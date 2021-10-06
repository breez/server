package main

import (
	"context"
	"encoding/hex"
	"io"
	"log"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/lightningnetwork/lnd/lnrpc"
	"go.starlark.net/starlark"
)

func isAccepted(acceptScript string, nodePubKey []byte, amt uint64) (bool, string) {
	log.Printf("isAccepted nid: %x, amt: %v, script: %s", nodePubKey, amt, acceptScript)
	if len(acceptScript) == 0 {
		return false, "Internal error"
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
		return false, "Internal error"
	}
	s, ok := value.(starlark.Tuple)
	if !ok {
		log.Println("starlark.Eval result is not a tuple")
		return false, "Internal error"
	}
	if s.Len() != 2 {
		log.Println("starlark.Eval tuple result length != 2")
		return false, "Internal error"
	}
	accepted, ok := s.Index(0).(starlark.Bool)
	if !ok {
		log.Println("starlark.Eval tuple first element is not a boolean")
		return false, "Internal error"
	}
	errorText, ok := s.Index(1).(starlark.String)
	if !ok {
		log.Println("starlark.Eval tuple second element is not a string")
		return false, "Internal error"
	}
	return bool(accepted.Truth()), errorText.String()
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

		resp := channelAcceptor(r, acceptScript)
		err = channelAcceptorClient.Send(resp)
		if err != nil {
			log.Printf("Error in channelAcceptorClient.Send(%v, %v): %v", r.PendingChanId, resp.Accept, err)
			return err
		}
	}
	return nil
}

func channelAcceptor(req *lnrpc.ChannelAcceptRequest, acceptScript string) *lnrpc.ChannelAcceptResponse {
	// Define the minimum dust limit we'll accept. This will be the dust
	// limit of the smallest P2WSH output considered standard.
	minDustLimit := btcutil.Amount(330)

	// Define the maximum dust limit we'll accept. This will be:
	// 3 * minDustLimit.
	maxDustLimit := minDustLimit * 3

	resp := &lnrpc.ChannelAcceptResponse{PendingChanId: req.PendingChanId}

	// Compare the received dust limit from OpenChannel against our bounds.
	receivedLimit := btcutil.Amount(req.DustLimit)
	if receivedLimit < minDustLimit || receivedLimit > maxDustLimit {
		// Reject the channel.
		resp.Accept = false
		return resp
	}
	accept, errorText := isAccepted(acceptScript, req.NodePubkey, req.FundingAmt)
	log.Printf("isAccepted returned: %v, %v", accept, errorText)
	resp.Accept = accept
	if !accept {
		resp.Error = errorText
	}
	return resp
}
