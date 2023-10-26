package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/breez/server/breez"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/lightningnetwork/lnd/lnwire"
)

var (
	ErrKeyNotSupported  = errors.New("key is not supported")
	ErrInvalidTimestamp = errors.New("invalid timestamp")
	allowedKeys         = map[string]struct{}{
		"routing_hints": {},
	}
	maxRequesTimeDiff = time.Second * 10
)

func verifyMessage(msg, pubKey, signature []byte) (bool, error) {

	if msg == nil {
		return false, fmt.Errorf("a message to verify MUST be passed in")
	}
	if signature == nil {
		return false, fmt.Errorf("a signature to verify MUST be passed in")
	}
	if pubKey == nil {
		return false, fmt.Errorf("a pubkey to verify MUST be passed in")
	}

	pubkey, err := btcec.ParsePubKey(pubKey)
	if err != nil {
		return false, fmt.Errorf("unable to parse pubkey: %v", err)
	}

	// The signature must be fixed-size LN wire format encoded.
	wireSig, err := lnwire.NewSigFromRawSignature(signature)
	if err != nil {
		return false, fmt.Errorf("failed to decode signature: %v", err)
	}
	sig, err := wireSig.ToSignature()
	if err != nil {
		return false, fmt.Errorf("failed to convert from wire format: %v", err)
	}

	// The signature is over the sha256 hash of the message.
	digest := chainhash.HashB(msg)
	valid := sig.Verify(digest, pubkey)
	return valid, nil
}

// SetNodeInfo sets the meeting information by the meeting moderator. The moderator provides a proof by signing the value
func (s *server) SetNodeInfo(ctx context.Context, in *breez.SetNodeInfoRequest) (*breez.SetNodeInfoResponse, error) {
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
	valid, err := verifyMessage([]byte(msg), in.Pubkey, in.Signature)
	if err != nil {
		return nil, err
	}

	if !valid {
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
