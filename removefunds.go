package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/lightningnetwork/lnd/lnrpc"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/metadata"
)

const (
	removeFundTimeout = 3600
)

var payReqGroup singleflight.Group

func createRemoveFundPaymentRequest(amount int64, address string) (string, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	addInoiceResp, err := client.AddInvoice(clientCtx, &lnrpc.Invoice{Value: amount, Memo: "Bitcoin Transfer", Expiry: removeFundTimeout})
	if err != nil {
		log.Printf("createPaymentRequest: failed to add invoice %v", err)
		return "", err
	}
	payReqHash := addInoiceResp.RHash
	err = updateKeyFields(hex.EncodeToString(payReqHash[:]), map[string]string{"address": address})
	return addInoiceResp.PaymentRequest, err
}

func ensureOnChainPaymentSent(payReqHash string) (string, error) {
	txID, err, _ := payReqGroup.Do(payReqHash, func() (interface{}, error) {
		return sendCoinsForReceivedPayment(payReqHash)
	})
	return txID.(string), err
}

func sendCoinsForReceivedPayment(payReqHash string) (string, error) {
	redisConn := redisPool.Get()
	defer redisConn.Close()

	removeFundRequest, err := getKeyFields(payReqHash)
	if err != nil {
		log.Printf("error querying payment request hash, %v", err)
		return "", err
	}
	address := removeFundRequest["address"]
	txID := removeFundRequest["txid"]

	//no fund request associated with invoice, continue
	if address == "" {
		return "", fmt.Errorf("no address associated with hash: %v", payReqHash)
	}

	//if we already payed
	if txID != "" {
		return txID, nil
	}

	log.Printf("paying on chain to destination address: %v", address)

	//1. fetch the invoice and check settled amount
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	invoice, err := client.LookupInvoice(clientCtx, &lnrpc.PaymentHash{RHashStr: payReqHash})
	if err != nil {
		return "", err
	}
	if !invoice.Settled {
		return "", errors.New("fail to pay, haven't received any payment")
	}

	//2. send coins to the user
	response, err := client.SendCoins(clientCtx, &lnrpc.SendCoinsRequest{Addr: address, Amount: invoice.AmtPaidSat, TargetConf: 48})
	if err != nil {
		return "", fmt.Errorf("fail to send coins: %v", err)
	}

	log.Printf("successfully sent coins to address %v", address)

	//3. save txId in fund request
	if err := updateKeyFields(payReqHash, map[string]string{"txid": response.Txid}); err != nil {
		log.Printf("Fail to save tx id %v associated with invoice %v", response.Txid, invoice.PaymentRequest)
	}

	return response.Txid, nil
}
