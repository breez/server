package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"google.golang.org/grpc/metadata"
)

const (
	minFees = 1112
)

var (
	feeEstimates string
)

func genFeeEstimates(hash string) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	var confTargets = []uint32{2, 3, 4, 5, 6, 10, 20, 25, 144, 504, 1008}
	feeByBlockTarget := make(map[uint32]uint32)
	for _, ct := range confTargets {
		r, err := walletKitClient.EstimateFee(clientCtx, &walletrpc.EstimateFeeRequest{ConfTarget: int32(ct)})
		if err != nil {
			log.Printf("walletKitClient.EstimateFee(%v): %v", ct, err)
			return
		}
		fees := uint32(r.SatPerKw * blockchain.WitnessScaleFactor)
		if fees < minFees {
			fees = minFees
		}
		feeByBlockTarget[ct] = fees
	}
	b, err := json.Marshal(struct {
		CurrentBlockHash string            `json:"current_block_hash"`
		FeeByBlockTarget map[uint32]uint32 `json:"fee_by_block_target"`
	}{hash, feeByBlockTarget})
	if err != nil {
		log.Printf("json.Marshal(%v): %v", feeByBlockTarget, err)
	}
	feeEstimates = string(b)
	log.Printf("Fees: %v", feeEstimates)
}

func startFeeEstimates() {
	go func() {
		for {
			clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
			stream, err := chainNotifierClient.RegisterBlockEpochNtfn(clientCtx, &chainrpc.BlockEpoch{})
			if err != nil {
				log.Printf("startFeeEstimates: chainNotifierClient.RegisterBlockEpochNtfn: %v", err)
				<-time.After(time.Second * 10)
				continue
			}

			for {
				block, err := stream.Recv()
				if err != nil {
					log.Printf("startFeeEstimates: stream.Recv: %v", err)
					break
				}
				c, err := chainhash.NewHash(block.Hash)
				if err != nil {
					log.Printf("startFeeEstimates: chainhash.NewHash(%x) err: %v", block.Hash, err)
					continue
				}
				genFeeEstimates(c.String())
			}
		}
	}()
}
