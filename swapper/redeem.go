package swapper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/submarineswaprpc"
	"google.golang.org/grpc/metadata"
)

const (
	MinConfirmations             = 6
	RedeemWitnessInputSize int32 = 1 + 1 + 73 + 1 + 32 + 1 + 100
)

type InProgressRedeem struct {
	PaymentHash        string
	Preimage           *string
	LockHeight         int32
	ConfirmationHeight int32
	Utxos              []string
	RedeemTxids        []string
}

type Redeemer struct {
	network               *chaincfg.Params
	ssClient              lnrpc.LightningClient
	ssRouterClient        routerrpc.RouterClient
	subswapClient         submarineswaprpc.SubmarineSwapperClient
	feeService            *FeeService
	updateSubswapTxid     func(paymentHash, txid string) error
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error
	getInProgressRedeems  func(blockheight int32) ([]*InProgressRedeem, error)
	setSubswapConfirmed   func(paymentHash string) error
}

func NewRedeemer(
	network *chaincfg.Params,
	ssClient lnrpc.LightningClient,
	ssRouterClient routerrpc.RouterClient,
	subswapClient submarineswaprpc.SubmarineSwapperClient,
	feeService *FeeService,
	updateSubswapTxid func(paymentHash, txid string) error,
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error,
	getInProgressRedeems func(blockheight int32) ([]*InProgressRedeem, error),
	setSubswapConfirmed func(paymentHash string) error,
) *Redeemer {
	return &Redeemer{
		network:               network,
		ssClient:              ssClient,
		ssRouterClient:        ssRouterClient,
		subswapClient:         subswapClient,
		feeService:            feeService,
		updateSubswapTxid:     updateSubswapTxid,
		updateSubswapPreimage: updateSubswapPreimage,
		getInProgressRedeems:  getInProgressRedeems,
		setSubswapConfirmed:   setSubswapConfirmed,
	}
}

func (r *Redeemer) Start(ctx context.Context) {
	log.Printf("REDEEM - before r.watchRedeemTxns()")
	go r.watchRedeemTxns(ctx)
}

func (r *Redeemer) watchRedeemTxns(ctx context.Context) {
	for {
		log.Printf("REDEEM - before checkRedeems()")
		r.checkRedeems()

		select {
		case <-time.After(time.Minute * 5):
		case <-ctx.Done():
			return
		}
	}
}

func (r *Redeemer) checkRedeems() {
	log.Printf("REDEEM - checkRedeems() begin")

	subswapClientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("SUBSWAPPER_LND_MACAROON_HEX"))

	info, err := r.ssClient.GetInfo(subswapClientCtx, &lnrpc.GetInfoRequest{})
	if err != nil {
		log.Printf("Failed to GetInfo from subswap node: %v", err)
		return
	}

	log.Printf("REDEEM - checkRedeems() before r.getInProgressRedeems(%v)", int32(info.BlockHeight))
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

	log.Printf("REDEEM - checkRedeems() before GetTransactions(%v)", syncHeight)
	txns, err := r.ssClient.GetTransactions(subswapClientCtx, &lnrpc.GetTransactionsRequest{
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
		log.Printf("REDEEM - checkRedeems() before checkRedeem(%v)", inProgressRedeem.PaymentHash)
		err = r.checkRedeem(int32(info.BlockHeight), inProgressRedeem, txMap)
		if err != nil {
			log.Printf("checkRedeem - payment hash %s failed: %v", inProgressRedeem.PaymentHash, err)
		}
	}
}

func (r *Redeemer) checkRedeem(blockHeight int32, inProgressRedeem *InProgressRedeem, txMap map[string]*lnrpc.Transaction) error {
	log.Printf("checkRedeem - payment hash %s", inProgressRedeem.PaymentHash)
	var txns []*lnrpc.Transaction
	for _, txid := range inProgressRedeem.RedeemTxids {
		tx, ok := txMap[txid]
		if !ok {
			continue
		}

		txns = append(txns, tx)
	}

	if inProgressRedeem.Preimage == nil {
		return fmt.Errorf("preimage not found for payment hash %s", inProgressRedeem.PaymentHash)
	}

	preimageStr := *inProgressRedeem.Preimage
	preimage, err := hex.DecodeString(preimageStr)
	if err != nil {
		return fmt.Errorf("failed to hex decode preimage: %w", err)
	}

	blocksLeft := inProgressRedeem.LockHeight - (blockHeight - inProgressRedeem.ConfirmationHeight)
	// Always redeem if there is no redeem tx yet.
	if len(txns) == 0 {
		log.Printf("RedeemWithinBlocks - preimage: %x, blocksLeft: %v", preimage, blocksLeft)
		return nil
		// _, err := r.RedeemWithinBlocks(preimage, blocksLeft, inProgressRedeem.LockHeight)
		// return err
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

	satPerVbyte, err := r.feeService.GetFeeRate(blocksLeft, inProgressRedeem.LockHeight)
	if err != nil {
		log.Printf("failed to get redeem fee rate: %v", err)
		// If there is a problem getting the fees, try to bump the tx on best effort.
		log.Printf("RedeemWithinBlocks - preimage: %x, blocksLeft: %v", preimage, blocksLeft)
		return nil
		// _, err = r.RedeemWithinBlocks(preimage, blocksLeft, inProgressRedeem.LockHeight)
		// return err
	}

	if bestTxSatPerVbyte+1 >= float64(int64(satPerVbyte)) {
		// Fee has not increased enough, do nothing
		return nil
	}

	// Attempt to redeem again with the higher fees.
	log.Printf("RedeemWithFees - preimage: %x, blocksLeft: %v fee: %v", preimage, blocksLeft, satPerVbyte)
	return nil
	// _, err = r.RedeemWithFees(preimage, blocksLeft, int64(satPerVbyte))
	// return err
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

func (r *Redeemer) RedeemWithinBlocks(preimage []byte, blocks int32, locktime int32) (string, error) {
	rate, err := r.feeService.GetFeeRate(blocks, locktime)
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

func (r *Redeemer) RedeemWeight(utxos []*submarineswaprpc.UnspentAmountResponse_Utxo) (int32, error) {
	if len(utxos) == 0 {
		return 0, errors.New("no utxo")
	}

	redeemTx := wire.NewMsgTx(1)

	// Add the inputs without the witness and calculate the amount to redeem
	var amount btcutil.Amount
	for _, utxo := range utxos {
		txid, err := chainhash.NewHashFromStr(utxo.Txid)
		if err != nil {
			return 0, fmt.Errorf("failed to parse txid: %w", err)
		}
		outpoint := &wire.OutPoint{
			Hash:  *txid,
			Index: utxo.Index,
		}
		amount += btcutil.Amount(utxo.Amount)
		txIn := wire.NewTxIn(outpoint, nil, nil)
		txIn.Sequence = 0
		redeemTx.AddTxIn(txIn)
	}

	//Generate a random address
	privateKey, err := btcec.NewPrivateKey()
	if err != nil {
		return 0, err
	}
	redeemAddress, err := btcutil.NewAddressPubKey(privateKey.PubKey().SerializeCompressed(), r.network)
	if err != nil {
		return 0, err
	}
	// Add the single output
	redeemScript, err := txscript.PayToAddrScript(redeemAddress)
	if err != nil {
		return 0, err
	}
	txOut := wire.TxOut{PkScript: redeemScript}
	redeemTx.AddTxOut(&txOut)
	redeemTx.LockTime = uint32(833000) // fake block height

	// Calcluate the weight and the fee
	weight := 4*int32(redeemTx.SerializeSizeStripped()) + RedeemWitnessInputSize*int32(len(redeemTx.TxIn))
	return weight, nil
}
