package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/breez/server/breez"
	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc/metadata"
)

var (
	ErrKeyNotSupported  = errors.New("key is not supported")
	ErrInvalidTimestamp = errors.New("invalid timestamp")
	allowedKeys         = map[string]struct{}{
		"routing_hints": {},
	}
)

// SetNodeInfo sets the meeting information by the meeting moderator. The moderator provides a proof by signing the value
func (s *server) SetNodeInfo(ctx context.Context, in *breez.SetNodeInfoRequest) (*breez.SetNodeInfoResponse, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))

	if _, ok := allowedKeys[string(in.Key)]; !ok {
		return nil, ErrKeyNotSupported
	}
	if time.Now().Sub(time.Unix(in.Timestamp, 0)) > time.Second*10 {
		return nil, ErrInvalidTimestamp
	}

	// concatenate all request payload fields
	msg := fmt.Sprintf("%v-%v-%v", hex.EncodeToString(in.Key), hex.EncodeToString(in.Value), in.Timestamp)

	// Verify the message
	verificationResponse, err := client.VerifyMessage(clientCtx, &lnrpc.VerifyMessageRequest{
		Msg:       []byte(msg),
		Signature: in.Signature,
	})
	if err != nil {
		return nil, err
	}
	pubkeyBytes, err := hex.DecodeString(verificationResponse.Pubkey)
	if err != nil {
		return nil, err
	}

	if !verificationResponse.Valid || !bytes.Equal(pubkeyBytes, in.Pubkey) {
		return nil, errors.New("failed to verify value")
	}

	// Update the value in redis and set expiration of 1 hour.
	redisKey := fmt.Sprintf("%v-%v", string(in.Pubkey), string(in.Key))
	if err := updateKeyFields(redisKey, map[string]string{
		"value":     string(in.Value),
		"timestamp": strconv.FormatInt(in.Timestamp, 10),
		"signature": in.Signature,
	}); err != nil {
		return nil, fmt.Errorf("failed to update value")
	}
	if err := setKeyExpiration(redisKey, 3600); err != nil {
		return nil, err
	}
	return &breez.SetNodeInfoResponse{}, nil
}

// GetNodeInfo is used by other participants to get the meeting information. They should verify the message using th
// provided signature.
func (s *server) GetNodeInfo(ctx context.Context, in *breez.GetNodeInfoRequest) (*breez.GetNodeInfoResponse, error) {
	keyStr := string(in.Key)
	if _, ok := allowedKeys[keyStr]; !ok {
		return nil, ErrKeyNotSupported
	}

	redisKey := fmt.Sprintf("%v-%v", string(in.Pubkey), string(in.Key))
	fields, err := getKeyFields(redisKey)
	if err != nil {
		return nil, err
	}

	rawVal, ok := fields["value"]
	if !ok {
		return nil, fmt.Errorf("failed to get value")
	}
	signature, ok := fields["signature"]
	if !ok {
		return nil, fmt.Errorf("failed to get value")
	}
	timestampStr, ok := fields["timestamp"]
	if !ok {
		return nil, fmt.Errorf("failed to get value")
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to get value")
	}
	return &breez.GetNodeInfoResponse{
		Value:     []byte(rawVal),
		Timestamp: timestamp,
		Signature: signature,
	}, nil
}
