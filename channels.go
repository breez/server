package main

import (
	"context"
	"encoding/hex"
	"io"
	"log"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"
	"go.starlark.net/starlark"
)

func isAccepted(acceptScript string, nodePubKey []byte, amt uint64) bool {
	log.Printf("isAccepted nid: %x, amt: %v, script: %s", nodePubKey, amt, acceptScript)
	if len(acceptScript) == 0 {
		return false
	}

	//var value starlark.Value
	value, err := starlark.Eval(
		&starlark.Thread{},
		"",
		acceptScript,
		starlark.StringDict{
			"node": starlark.String(hex.EncodeToString(nodePubKey)),
			"amt":  starlark.MakeInt64(int64(amt)),
		},
	)
	if err != nil {
		if evalErr, ok := err.(*starlark.EvalError); ok {
			log.Printf("starlark.Eval() error: %v", evalErr.Backtrace())
		}
		log.Printf("starlark.Eval error: %v", err)
		return false
	}

	return bool(value.Truth())
}

func subscribeChannelAcceptor(ctx context.Context, c lnrpc.LightningClient, acceptScript string) {
	for {
		log.Println("new subscribe")
		err := subscribeChannelAcceptorOnce(ctx, c, acceptScript)
		if err != nil {
			log.Println("subscribeTransactions:", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func subscribeChannelAcceptorOnce(ctx context.Context, c lnrpc.LightningClient, acceptScript string) error {
	channelAcceptorClient, err := c.ChannelAcceptor(ctx)
	if err != nil {
		log.Println("ChannelAcceptor:", err)
		return err
	}
	for {
		log.Println("new Recv call")
		r, err := channelAcceptorClient.Recv()
		if err == io.EOF {
			log.Println("Stream stopped. Need to re-registser")
			break
		}
		if err != nil {
			log.Printf("Error in channelAcceptorClient.Recv(): %v", err)
			return err
		}
		accept := isAccepted(acceptScript, r.NodePubkey, r.FundingAmt)
		log.Printf("isAccepted returned: %v", accept)
		err = channelAcceptorClient.Send(&lnrpc.ChannelAcceptResponse{
			PendingChanId: r.PendingChanId,
			Accept:        accept,
		})
		if err != nil {
			log.Printf("Error in channelAcceptorClient.Send(%v, %v): %v", r.PendingChanId, accept, err)
			return err
		}
	}
	return nil
}
