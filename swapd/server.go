package swapd

import (
	"context"

	"github.com/breez/server/breez"
)

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
	return s.client.CreateSwap(ctx, in)
}

func (s *swapdServer) PaySwap(ctx context.Context, in *breez.PaySwapRequest) (*breez.PaySwapResponse, error) {
	return s.client.PaySwap(ctx, in)
}

func (s *swapdServer) RefundSwap(ctx context.Context, in *breez.RefundSwapRequest) (*breez.RefundSwapResponse, error) {
	return s.client.RefundSwap(ctx, in)
}

func (s *swapdServer) SwapParameters(ctx context.Context, in *breez.SwapParametersRequest) (*breez.SwapParametersResponse, error) {
	return s.client.SwapParameters(ctx, in)
}
