package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/breez/boltz"
	"github.com/breez/server/breez"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/google/uuid"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"google.golang.org/grpc/metadata"
)

func registerPastBoltzReverseSwapTxNotifications() error {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	chainInfo, err := client.GetInfo(clientCtx, &lnrpc.GetInfoRequest{})
	if err != nil {
		log.Printf("client.GetInfo(): %v", err)
		return fmt.Errorf("client.GetInfo(): %w", err)
	}
	rows, err := boltzReverseSwapToNotify(chainInfo.BlockHeight)
	if err != nil {
		log.Printf("boltzReverseSwapToNotify(%v): %v", chainInfo.BlockHeight, err)
		return fmt.Errorf("boltzReverseSwapToNotify(%v): %w", chainInfo.BlockHeight, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			u                     uuid.UUID
			boltzReverseSwapInfo  BoltzReverseSwapInfo
			title, body, deviceID string
			txHash, script        []byte
			blockHeightHint       uint32
		)
		err = rows.Scan(&u, &boltzReverseSwapInfo, &title, &body, &deviceID, &txHash, &script, &blockHeightHint)
		if err != nil {
			log.Printf("rows.Scan: %v", err)
			continue
		}
		log.Printf("u: %v, BoltzId: %v, TimeoutBlockHeight: %v, title: %v, body: %v, deviceID: %v, txHash: %x, script: %x, blockHeightHint: %v",
			u.String(), boltzReverseSwapInfo.ID, boltzReverseSwapInfo.TimeoutBlockHeight, title, body, deviceID, txHash, script, blockHeightHint)
		_, err := registerTxNotification(&u, &breez.PushTxNotificationRequest{
			DeviceId:        deviceID,
			Title:           title,
			Body:            body,
			TxHash:          txHash,
			Script:          script,
			BlockHeightHint: blockHeightHint,
			Info: &breez.PushTxNotificationRequest_BoltzReverseSwapLockupTxInfo{
				BoltzReverseSwapLockupTxInfo: &breez.BoltzReverseSwapLockupTx{
					BoltzId:            boltzReverseSwapInfo.ID,
					TimeoutBlockHeight: boltzReverseSwapInfo.TimeoutBlockHeight,
				},
			}},
		)
		if err != nil {
			log.Printf("registerTxNotification(%v): %v)", u.String(), err)
		}
	}
	return rows.Err()
}

func (s *server) RegisterTxNotification(ctx context.Context, in *breez.PushTxNotificationRequest) (*breez.PushTxNotificationResponse, error) {
	return registerTxNotification(nil, in)
}

func hashString(h []byte) string {
	ch, err := chainhash.NewHash(h)
	if err != nil {
		return ""
	}
	return ch.String()
}

func callFromBlockHeight(f func(), blockHeight uint32) {
	cancellableCtx, cancel := context.WithCancel(context.Background())
	clientCtx := metadata.AppendToOutgoingContext(cancellableCtx, "macaroon", os.Getenv("LND_MACAROON_HEX"))
	stream, err := chainNotifierClient.RegisterBlockEpochNtfn(clientCtx, &chainrpc.BlockEpoch{})
	if err != nil {
		log.Printf("chainNotifierClient.RegisterBlockEpochNtfn(): %v", err)
		cancel()
	}
	go func() {
		for {
			block, err := stream.Recv()
			if err != nil {
				log.Printf("stream.Recv: %v", err)
				return
			}
			if block.Height >= blockHeight {
				log.Printf("callFromBlockHeight: calling f() because: %v >= %v", block.Height, blockHeight)
				f()
				break
			}
		}
		cancel()
	}()
}

func registerTxNotification(u *uuid.UUID, in *breez.PushTxNotificationRequest) (*breez.PushTxNotificationResponse, error) {
	var txType int32
	var boltzReverseSwapInfo *BoltzReverseSwapInfo
	switch x := in.Info.(type) {
	case *breez.PushTxNotificationRequest_BoltzReverseSwapLockupTxInfo:
		boltzReverseSwapInfo = &BoltzReverseSwapInfo{
			ID:                 x.BoltzReverseSwapLockupTxInfo.BoltzId,
			TimeoutBlockHeight: x.BoltzReverseSwapLockupTxInfo.TimeoutBlockHeight,
		}
		txType = TypeBoltzReverseSwapLockup
	default:
		txType = TypeUnknown
	}
	if txType == TypeUnknown {
		return nil, errors.New("only boltz reverse swap lockup transactions supported")
	}
	var err error
	if u == nil {
		u, err = insertTxNotification(in)
		if err == nil && u == nil {
			return &breez.PushTxNotificationResponse{}, nil
		}
	}
	confRequest := &chainrpc.ConfRequest{
		NumConfs:   1,
		HeightHint: in.BlockHeightHint,
		Txid:       in.TxHash,
		Script:     in.Script,
	}
	cancellableCtx, cancel := context.WithCancel(context.Background())
	clientCtx := metadata.AppendToOutgoingContext(cancellableCtx, "macaroon", os.Getenv("LND_MACAROON_HEX"))
	stream, err := chainNotifierClient.RegisterConfirmationsNtfn(clientCtx, confRequest)
	if err != nil {
		log.Printf("chainNotifierClient.RegisterConfirmationsNtfn(%#v): %v", confRequest, err)
		cancel()
		return nil, fmt.Errorf("chainNotifierClient.RegisterConfirmationsNtfn(%#v): %w", confRequest, err)
	}
	go func() {
		defer cancel()
		var confDetails chainrpc.ConfDetails
		for {
			confEvent, err := stream.Recv()
			if err != nil {
				log.Printf("stream.Recv(): %v", err)
				return
			}
			confDetails = *confEvent.GetConf()
			log.Printf("UUID: %v block: (%v) %v, index: %v rawTX:%x", u,
				confDetails.BlockHeight, hashString(confDetails.BlockHash), confDetails.TxIndex, confDetails.RawTx)
			break
		}
		if txType == TypeBoltzReverseSwapLockup {
			_, _, tx, _, err := boltz.GetTransaction(boltzReverseSwapInfo.ID, "", 0)
			if err != nil {
				log.Printf("boltz.GetTransaction(%v): %v", boltzReverseSwapInfo.ID, err)
				return
			}
			if hex.EncodeToString(confDetails.RawTx) != tx {
				log.Printf("bad transaction: %x != %v", confDetails.RawTx, tx)
				return
			}
		}
		err = sendTxNotification(in, confRequest)
		if err != nil {
			log.Printf("sendTxNotification(%#v, %#v): %v", in, confRequest, err)
		}
		tx, _ := btcutil.NewTxFromBytes(confDetails.RawTx)
		var txHash chainhash.Hash
		if tx != nil {
			txHash = *tx.Hash()
		}

		err := txNotified(*u, txHash, confDetails.RawTx, confDetails.BlockHeight, confDetails.BlockHash, confDetails.TxIndex)
		log.Printf("txNotified(%v, %v, %x, %v, %x, %v): %v", *u, txHash.String(), confDetails.RawTx, confDetails.BlockHeight, confDetails.BlockHash, confDetails.TxIndex, err)
	}()
	if txType == TypeBoltzReverseSwapLockup {
		callFromBlockHeight(cancel, boltzReverseSwapInfo.TimeoutBlockHeight)
	}
	return &breez.PushTxNotificationResponse{}, nil
}

func sendTxNotification(in *breez.PushTxNotificationRequest, confRequest *chainrpc.ConfRequest) error {
	data := make(map[string]string)
	err := notifyAlertMessage(in.Title, in.Body, data, in.DeviceId)

	return err
}
