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
	"slices"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/submarineswaprpc"
	"google.golang.org/grpc/metadata"
)

type Redeemer struct {
	ssClient              lnrpc.LightningClient
	subswapClient         submarineswaprpc.SubmarineSwapperClient
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error
	updateSubswapTxid     func(paymentHash, txid string) error
	currentSatPerVbyte    float64
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
		satPerVbyte, err := r.getCurrentChainFeeSatPerVbyte()
		if err != nil {
			log.Printf("failed to get current chain fee rate: %v", err)
		} else {
			r.currentSatPerVbyte = satPerVbyte
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

func (r *Redeemer) getCurrentChainFeeSatPerVbyte() (float64, error) {
	now := time.Now().Unix()
	cacheBust := (now / 300) * 300
	resp, err := http.Get(
		fmt.Sprintf("https://whatthefee.io/data.json?c=%d", cacheBust),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to call whatthefee.io: %v", err)
	}
	defer resp.Body.Close()

	var body whatthefeeBody
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		return 0, fmt.Errorf("failed to decode whatthefee.io response: %w", err)
	}

	rowIndex := slices.IndexFunc(body.Index, func(i int32) bool {
		return i < 24 && i > 12
	})
	if rowIndex < 0 {
		return 0, fmt.Errorf("could not find fee rate index in whatthefee.io response")
	}

	columnIndex := slices.IndexFunc(body.Columns, func(s string) bool {
		f, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return false
		}
		return f > 0.9
	})
	if columnIndex < 0 {
		return 0, fmt.Errorf("could not find fee rate column in whatthefee.io response")
	}

	if rowIndex >= len(body.Data) {
		return 0, fmt.Errorf("could not find fee rate column in whatthefee.io response")
	}
	row := body.Data[rowIndex]
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
	weight, preimage, err := parseRedeemTx(tx)
	if err != nil {
		// Not a redeem tx
		return
	}

	if tx.NumConfirmations > 0 {
		// Do nothing
		return
	}

	satPerVbyte := float64(tx.TotalFees) * 4 / float64(weight)
	if int64(satPerVbyte) >= int64(r.currentSatPerVbyte) {
		// Fee has not increased enough, do nothing
		return
	}

	// Attempt to redeem again. Redeem will fail if an existing tx cannot be replaced, but that's fine.
	r.Redeem(preimage)
}

func parseRedeemTx(tx *lnrpc.Transaction) (int64, []byte, error) {
	//rawTx contain the hex decode raw tx
	rawTx, err := hex.DecodeString(tx.RawTxHex)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to hex decode tx: %w", err)
	}
	msgTx := wire.NewMsgTx(wire.TxVersion)
	err = msgTx.Deserialize(bytes.NewReader(rawTx))
	if err != nil {
		return 0, nil, fmt.Errorf("failed to deserialize tx: %w", err)
	}

	if len(msgTx.TxIn) != 1 {
		// In the Redeem tx, there is only one TxIn
		return 0, nil, fmt.Errorf("not a redeem tx")
	}

	txin := msgTx.TxIn[0]
	if len(txin.Witness) < 2 {
		return 0, nil, fmt.Errorf("not a redeem tx")
	}

	preimage := txin.Witness[1]
	if preimage == nil {
		return 0, nil, fmt.Errorf("no preimage")
	}

	utilTx := btcutil.NewTx(msgTx)
	weight := blockchain.GetTransactionWeight(utilTx)
	return weight, preimage, nil
}

func (r *Redeemer) Redeem(preimage []byte) (string, error) {
	return r.doRedeem(preimage, 30, int64(r.currentSatPerVbyte))
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
