package swapd

import (
	"context"
	"errors"
	"os"
	"strings"

	"slices"

	"github.com/breez/server/auth"
	"github.com/breez/server/breez"
)

var errNoTaprootKey = errors.New("not a taproot swap api key")

type swapdServer struct {
	client breez.TaprootSwapperClient
	breez.UnimplementedTaprootSwapperServer
}

func NewServer(client breez.TaprootSwapperClient) breez.TaprootSwapperServer {
	return &swapdServer{
		client: client,
	}
}

func (s *swapdServer) CreateSwap(ctx context.Context, in *breez.CreateSwapRequest) (*breez.CreateSwapResponse, error) {
	err := ensureTaprootSwapKey(ctx)
	if err != nil {
		return nil, err
	}
	return s.client.CreateSwap(ctx, in)
}

func (s *swapdServer) PaySwap(ctx context.Context, in *breez.PaySwapRequest) (*breez.PaySwapResponse, error) {
	err := ensureTaprootSwapKey(ctx)
	if err != nil {
		return nil, err
	}
	return s.client.PaySwap(ctx, in)
}

func (s *swapdServer) RefundSwap(ctx context.Context, in *breez.RefundSwapRequest) (*breez.RefundSwapResponse, error) {
	err := ensureTaprootSwapKey(ctx)
	if err != nil {
		return nil, err
	}
	return s.client.RefundSwap(ctx, in)
}

func (s *swapdServer) SwapParameters(ctx context.Context, in *breez.SwapParametersRequest) (*breez.SwapParametersResponse, error) {
	err := ensureTaprootSwapKey(ctx)
	if err != nil {
		return nil, err
	}
	return s.client.SwapParameters(ctx, in)
}

func ensureTaprootSwapKey(ctx context.Context) error {
	keysStr := os.Getenv("TAPROOT_API_KEYS")
	if keysStr == "" {
		return errNoTaprootKey
	}

	validKeys := strings.Split(keysStr, ",")
	clientKeys := auth.GetHeaderKeys(ctx)
	for _, key := range clientKeys {
		if slices.Contains(validKeys, key) {
			return nil
		}
	}

	return errNoTaprootKey
}
