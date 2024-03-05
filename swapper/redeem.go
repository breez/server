package swapper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/submarineswaprpc"
	"google.golang.org/grpc/metadata"
)

const SwapLockTime = 288
const MinConfirmations = 6

type InProgressRedeem struct {
	PaymentHash        string
	Preimage           *string
	LockHeight         int32
	ConfirmationHeight int32
	Utxos              []string
	RedeemTxids        []string
}

type Redeemer struct {
	ssClient              lnrpc.LightningClient
	ssRouterClient        routerrpc.RouterClient
	subswapClient         submarineswaprpc.SubmarineSwapperClient
	updateSubswapTxid     func(paymentHash, txid string) error
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error
	getInProgressRedeems  func(blockheight int32) ([]*InProgressRedeem, error)
	setSubswapConfirmed   func(paymentHash string) error
	feesLastUpdated       time.Time
	currentFees           *whatthefeeBody
	mtx                   sync.RWMutex
}

func NewRedeemer(
	ssClient lnrpc.LightningClient,
	ssRouterClient routerrpc.RouterClient,
	subswapClient submarineswaprpc.SubmarineSwapperClient,
	updateSubswapTxid func(paymentHash, txid string) error,
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error,
	getInProgressRedeems func(blockheight int32) ([]*InProgressRedeem, error),
	setSubswapConfirmed func(paymentHash string) error,
) *Redeemer {
	return &Redeemer{
		ssClient:              ssClient,
		ssRouterClient:        ssRouterClient,
		subswapClient:         subswapClient,
		updateSubswapTxid:     updateSubswapTxid,
		updateSubswapPreimage: updateSubswapPreimage,
		getInProgressRedeems:  getInProgressRedeems,
		setSubswapConfirmed:   setSubswapConfirmed,
	}
}

func (r *Redeemer) Start(ctx context.Context) {
	go r.watchRedeemTxns(ctx)
	go r.watchFeeRate(ctx)
}

func (r *Redeemer) watchFeeRate(ctx context.Context) {
	for {
		now := time.Now()
		fees, err := r.getFees()
		if err != nil {
			log.Printf("failed to get current chain fee rates: %v", err)
		} else {
			r.mtx.Lock()
			r.currentFees = fees
			r.feesLastUpdated = now
			r.mtx.Unlock()
		}

		select {
		case <-time.After(time.Minute * 5):
		case <-ctx.Done():
			return
		}
	}
}

func (r *Redeemer) watchRedeemTxns(ctx context.Context) {
	for {
		r.checkRedeems()

		select {
		case <-time.After(time.Minute * 5):
		case <-ctx.Done():
			return
		}
	}
}

type whatthefeeBody struct {
	Index   []int32   `json:"index"`
	Columns []string  `json:"columns"`
	Data    [][]int32 `json:"data"`
}

func (r *Redeemer) getFees() (*whatthefeeBody, error) {
	now := time.Now().Unix()
	cacheBust := (now / 300) * 300
	resp, err := http.Get(
		fmt.Sprintf("https://whatthefee.io/data.json?c=%d", cacheBust),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to call whatthefee.io: %v", err)
	}
	defer resp.Body.Close()

	var body whatthefeeBody
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		return nil, fmt.Errorf("failed to decode whatthefee.io response: %w", err)
	}

	return &body, nil
}

func (r *Redeemer) getFeeRate(blocks int32) (float64, error) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	if len(r.currentFees.Index) < 1 {
		return 0, fmt.Errorf("empty row index")
	}

	// get the block between 0 and SwapLockTime
	b := math.Min(math.Max(0, float64(blocks)), SwapLockTime)

	// certainty is linear between 0.5 and 1 based on the amount of blocks left
	certainty := 0.5 + (((SwapLockTime - b) / SwapLockTime) / 2)

	// Get the row closest to the amount of blocks left
	rowIndex := 0
	prevRow := r.currentFees.Index[rowIndex]
	for i := 1; i < len(r.currentFees.Index); i++ {
		current := r.currentFees.Index[i]
		if math.Abs(float64(current)-b) < math.Abs(float64(prevRow)-b) {
			rowIndex = i
			prevRow = current
		}
	}

	if len(r.currentFees.Columns) < 1 {
		return 0, fmt.Errorf("empty column index")
	}

	// Get the column closest to the certainty
	columnIndex := 0
	prevColumn, err := strconv.ParseFloat(r.currentFees.Columns[columnIndex], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid column content '%s'", r.currentFees.Columns[columnIndex])
	}
	for i := 1; i < len(r.currentFees.Columns); i++ {
		current, err := strconv.ParseFloat(r.currentFees.Columns[i], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid column content '%s'", r.currentFees.Columns[i])
		}
		if math.Abs(current-certainty) < math.Abs(prevColumn-certainty) {
			columnIndex = i
			prevColumn = current
		}
	}

	if rowIndex >= len(r.currentFees.Data) {
		return 0, fmt.Errorf("could not find fee rate column in whatthefee.io response")
	}
	row := r.currentFees.Data[rowIndex]
	if columnIndex >= len(row) {
		return 0, fmt.Errorf("could not find fee rate column in whatthefee.io response")
	}

	rate := row[columnIndex]
	satPerVByte := math.Exp(float64(rate) / 100)
	return satPerVByte, nil
}

func (r *Redeemer) checkRedeems() {
	info, err := r.ssClient.GetInfo(context.Background(), &lnrpc.GetInfoRequest{})
	if err != nil {
		log.Printf("Failed to GetInfo from subswap node: %v", err)
		return
	}

	inProgressRedeems, err := r.getInProgressRedeems(int32(info.BlockHeight))
	if err != nil {
		log.Printf("Failed to get in progress redeems: %v", err)
		return
	}

	syncHeight := int32(info.BlockHeight)
	for _, inProgressRedeem := range inProgressRedeems {
		if inProgressRedeem.ConfirmationHeight < syncHeight {
			syncHeight = inProgressRedeem.ConfirmationHeight
		}
	}

	txns, err := r.ssClient.GetTransactions(context.Background(), &lnrpc.GetTransactionsRequest{
		StartHeight: syncHeight,
		EndHeight:   -1,
	})
	if err != nil {
		log.Printf("Failed to GetTransactions from subswap node: %v", err)
		return
	}

	txMap := make(map[string]*lnrpc.Transaction, 0)
	for _, tx := range txns.Transactions {
		txMap[tx.TxHash] = tx
	}

	for _, inProgressRedeem := range inProgressRedeems {
		err = r.checkRedeem(int32(info.BlockHeight), inProgressRedeem, txMap)
		if err != nil {
			log.Printf("checkRedeem - payment hash %s failed: %v", inProgressRedeem.PaymentHash, err)
		}
	}
}

func (r *Redeemer) checkRedeem(blockHeight int32, inProgressRedeem *InProgressRedeem, txMap map[string]*lnrpc.Transaction) error {
	var txns []*lnrpc.Transaction
	for _, txid := range inProgressRedeem.RedeemTxids {
		tx, ok := txMap[txid]
		if !ok {
			continue
		}

		txns = append(txns, tx)
	}

	preimageStr, err := r.tryGetPreimage(inProgressRedeem)
	if err != nil {
		return err
	}

	preimage, err := hex.DecodeString(preimageStr)
	if err != nil {
		return fmt.Errorf("failed to hex decode preimage: %w", err)
	}

	blocksLeft := inProgressRedeem.LockHeight - (blockHeight - inProgressRedeem.ConfirmationHeight)
	// Always redeem if there is no redeem tx yet.
	if len(txns) == 0 {
		_, err := r.RedeemWithinBlocks(preimage, blocksLeft)
		return err
	}

	var bestTxSatPerVbyte float64
	for _, tx := range txns {
		if tx.NumConfirmations > MinConfirmations {
			err = r.setSubswapConfirmed(inProgressRedeem.PaymentHash)
			if err != nil {
				log.Printf(
					"failed to set subswap payment hash '%s' confirmed: %v",
					inProgressRedeem.PaymentHash,
					err,
				)
			}
			return nil
		}

		if tx.NumConfirmations > 0 {
			// Do nothing
			return nil
		}

		weight, err := getWeight(tx)
		if err != nil {
			return fmt.Errorf("failed to compute tx weight: %w", err)
		}

		currentTxSatPerVbyte := float64(tx.TotalFees) * 4 / float64(weight)
		if currentTxSatPerVbyte > bestTxSatPerVbyte {
			bestTxSatPerVbyte = currentTxSatPerVbyte
		}
	}

	satPerVbyte, err := r.getFeeRate(blocksLeft)
	if err != nil {
		log.Printf("failed to get redeem fee rate: %v", err)
		// If there is a problem getting the fees, try to bump the tx on best effort.
		_, err = r.RedeemWithinBlocks(preimage, blocksLeft)
		return err
	}

	if bestTxSatPerVbyte+1 >= float64(int64(satPerVbyte)) {
		// Fee has not increased enough, do nothing
		return nil
	}

	// Attempt to redeem again with the higher fees.
	_, err = r.RedeemWithFees(preimage, blocksLeft, int64(satPerVbyte))
	return err
}

func (r *Redeemer) tryGetPreimage(inProgressRedeem *InProgressRedeem) (string, error) {
	if inProgressRedeem.Preimage != nil {
		return *inProgressRedeem.Preimage, nil
	}

	ph, err := hex.DecodeString(inProgressRedeem.PaymentHash)
	if err != nil {
		return "", fmt.Errorf("failed to hex decode payment hash: %w", err)
	}

	// If there is no preimage, check whether the preimage is on the node
	tp, err := r.ssRouterClient.TrackPaymentV2(
		context.Background(),
		&routerrpc.TrackPaymentRequest{
			PaymentHash: ph,
		},
	)
	if err != nil {
		return "", fmt.Errorf("failed to lookup payment: %w", err)
	}

	// TODO: Confirm this call doesn't block if the payment is not found
	paymentInfo, err := tp.Recv()
	if err != nil {
		return "", fmt.Errorf("failed to lookup payment: %w", err)
	}

	if paymentInfo.PaymentPreimage == "" {
		return "", fmt.Errorf("no preimage found")
	}

	err = r.updateSubswapPreimage(inProgressRedeem.PaymentHash, paymentInfo.PaymentPreimage)
	if err != nil {
		log.Printf(
			"failed to update subswap preimage '%s' for payment hash '%s'. error: %v",
			paymentInfo.PaymentPreimage,
			inProgressRedeem.PaymentHash,
			err)
	}

	return paymentInfo.PaymentPreimage, nil
}

func getWeight(tx *lnrpc.Transaction) (int64, error) {
	//rawTx contain the hex decode raw tx
	rawTx, err := hex.DecodeString(tx.RawTxHex)
	if err != nil {
		return 0, fmt.Errorf("failed to hex decode tx: %w", err)
	}
	msgTx := wire.NewMsgTx(wire.TxVersion)
	err = msgTx.Deserialize(bytes.NewReader(rawTx))
	if err != nil {
		return 0, fmt.Errorf("failed to deserialize tx: %w", err)
	}

	utilTx := btcutil.NewTx(msgTx)
	weight := blockchain.GetTransactionWeight(utilTx)
	return weight, nil
}

func (r *Redeemer) RedeemWithinBlocks(preimage []byte, blocks int32) (string, error) {
	rate, err := r.getFeeRate(blocks)
	if err != nil {
		log.Printf("RedeemWithinBlocks(%x, %d) - getFeeRate error: %v", preimage, blocks, err)
	}

	return r.doRedeem(preimage, blocks, int64(rate))
}

func (r *Redeemer) RedeemWithFees(preimage []byte, targetConf int32, satPerVbyte int64) (string, error) {
	return r.doRedeem(preimage, targetConf, satPerVbyte)
}

func (r *Redeemer) doRedeem(preimage []byte, targetConf int32, satPerByte int64) (string, error) {
	ph := sha256.Sum256(preimage)

	if targetConf > 0 && satPerByte > 0 {
		targetConf = 0
	}

	subswapClientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("SUBSWAPPER_LND_MACAROON_HEX"))
	redeem, err := r.subswapClient.SubSwapServiceRedeem(subswapClientCtx, &submarineswaprpc.SubSwapServiceRedeemRequest{
		Preimage:   preimage,
		TargetConf: targetConf,
		SatPerByte: satPerByte,
	})
	if err != nil {
		log.Printf("doRedeem - couldn't redeem funds for preimage: %x, targetConf: %d, satPerByte %d, error: %v", preimage, targetConf, satPerByte, err)
		return "", err
	}

	log.Printf("doRedeem - redeem tx broadcast: %s", redeem.Txid)
	err = r.updateSubswapTxid(hex.EncodeToString(ph[:]), redeem.Txid)
	if err != nil {
		log.Printf("doRedeem - updateSubswapTxid paymentHash: %x, txid: %s, error: %v", ph, redeem.Txid, err)
	}

	return redeem.Txid, err
}
