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

	"github.com/breez/server/bitcoind"
	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/submarineswaprpc"
	"google.golang.org/grpc/metadata"
)

const SwapLockTime = 288

type Redeemer struct {
	ssClient              lnrpc.LightningClient
	subswapClient         submarineswaprpc.SubmarineSwapperClient
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error
	updateSubswapTxid     func(paymentHash, txid string) error
	feesLastUpdated       time.Time
	currentFees           *whatthefeeBody
	mtx                   sync.RWMutex
}

func NewRedeemer(
	ssClient lnrpc.LightningClient,
	subswapClient submarineswaprpc.SubmarineSwapperClient,
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error,
	updateSubswapTxid func(paymentHash, txid string) error,
) *Redeemer {
	return &Redeemer{
		ssClient:              ssClient,
		subswapClient:         subswapClient,
		updateSubswapPreimage: updateSubswapPreimage,
		updateSubswapTxid:     updateSubswapTxid,
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

	unconfirmed, err := r.ssClient.GetTransactions(context.Background(), &lnrpc.GetTransactionsRequest{
		StartHeight: int32(info.BlockHeight) + 1,
		EndHeight:   -1,
	})
	if err != nil {
		log.Printf("Failed to GetTransactions from subswap node: %v", err)
		return
	}

	for _, tx := range unconfirmed.Transactions {
		r.checkRedeem(tx)
	}
}

func (r *Redeemer) checkRedeem(tx *lnrpc.Transaction) {
	input, weight, preimage, err := parseRedeemTx(tx)
	if err != nil {
		// Not a redeem tx
		return
	}

	if tx.NumConfirmations > 0 {
		// Do nothing
		return
	}

	var satPerVbyte float64
	prevSatPerVbyte := float64(tx.TotalFees) * 4 / float64(weight)
	txin, err := bitcoind.GetTransaction(input.Hash.String())
	if err == nil {
		satPerVbyte, err = r.getFeeRate(int32(SwapLockTime - txin.Confirmations))
	} else {
		satPerVbyte, err = r.getFeeRate(30)
	}

	if err != nil {
		log.Printf("failed to get redeem fee rate: %v", err)
		r.RedeemWithinBlocks(preimage, 30)
		return
	}

	if prevSatPerVbyte+1 >= satPerVbyte {
		// Fee has not increased enough, do nothing
		return
	}

	// Attempt to redeem again with the higher fees.
	r.RedeemWithFees(preimage, 30, int64(satPerVbyte))
}

func parseRedeemTx(tx *lnrpc.Transaction) (*wire.OutPoint, int64, []byte, error) {
	//rawTx contain the hex decode raw tx
	rawTx, err := hex.DecodeString(tx.RawTxHex)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to hex decode tx: %w", err)
	}
	msgTx := wire.NewMsgTx(wire.TxVersion)
	err = msgTx.Deserialize(bytes.NewReader(rawTx))
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to deserialize tx: %w", err)
	}

	if len(msgTx.TxIn) != 1 {
		// In the Redeem tx, there is only one TxIn
		return nil, 0, nil, fmt.Errorf("not a redeem tx")
	}

	txin := msgTx.TxIn[0]
	if len(txin.Witness) < 2 {
		return nil, 0, nil, fmt.Errorf("not a redeem tx")
	}

	preimage := txin.Witness[1]
	if preimage == nil {
		return nil, 0, nil, fmt.Errorf("no preimage")
	}

	utilTx := btcutil.NewTx(msgTx)
	weight := blockchain.GetTransactionWeight(utilTx)
	return &txin.PreviousOutPoint, weight, preimage, nil
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
	err := r.updateSubswapPreimage(hex.EncodeToString(ph[:]), hex.EncodeToString(preimage))
	if err != nil {
		log.Printf("Failed to update subswap preimage '%x' for payment hash '%x', error: %v", preimage, ph, err)
	}

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
