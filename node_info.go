package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/breez/server/breez"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"google.golang.org/grpc/metadata"
)

var (
	ErrKeyNotSupported  = errors.New("key is not supported")
	ErrInvalidTimestamp = errors.New("invalid timestamp")
	allowedKeys         = map[string]struct{}{
		"routing_hints": {},
	}
	maxRequesTimeDiff = time.Second * 10
)

// SetNodeInfo sets the meeting information by the meeting moderator. The moderator provides a proof by signing the value
func (s *server) SetNodeInfo(ctx context.Context, in *breez.SetNodeInfoRequest) (*breez.SetNodeInfoResponse, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))

	if _, ok := allowedKeys[in.Key]; !ok {
		return nil, ErrKeyNotSupported
	}
	requestTimeDiff := time.Since(time.Unix(in.Timestamp, 0))
	if math.Abs(float64(requestTimeDiff)) > float64(maxRequesTimeDiff) {
		return nil, ErrInvalidTimestamp
	}

	// concatenate all request payload fields
	msg := fmt.Sprintf("%v-%v-%v", in.Key, hex.EncodeToString(in.Value), in.Timestamp)

	// Verify the message
	verificationResponse, err := signerClient.VerifyMessage(clientCtx, &signrpc.VerifyMessageReq{
		Msg:       []byte(msg),
		Pubkey:    in.Pubkey,
		Signature: in.Signature,
	})
	if err != nil {
		return nil, err
	}

	if !verificationResponse.Valid {
		return nil, errors.New("failed to verify value")
	}

	// Update the value in redis and set expiration of 1 hour.
	redisKey := fmt.Sprintf("%v-%v", hex.EncodeToString(in.Pubkey), in.Key)
	if err := updateKeyFields(redisKey, map[string]string{
		"value":     hex.EncodeToString(in.Value),
		"timestamp": strconv.FormatInt(in.Timestamp, 10),
		"signature": hex.EncodeToString(in.Signature),
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
	if _, ok := allowedKeys[in.Key]; !ok {
		return nil, ErrKeyNotSupported
	}

	redisKey := fmt.Sprintf("%v-%v", hex.EncodeToString(in.Pubkey), in.Key)
	fields, err := getKeyFields(redisKey)
	if err != nil {
		return nil, err
	}

	value, ok := fields["value"]
	if !ok {
		return nil, fmt.Errorf("failed to get value")
	}
	valueBytes, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("failed to get value")
	}

	signature, ok := fields["signature"]
	if !ok {
		return nil, fmt.Errorf("failed to get value")
	}
	signatureBytes, err := hex.DecodeString(signature)
	if err != nil {
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
		Value:     valueBytes,
		Timestamp: timestamp,
		Signature: signatureBytes,
	}, nil
}
