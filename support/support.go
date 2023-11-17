package support

import (
	"context"
	"errors"

	breezrpc "github.com/breez/server/breez"
)

// Server implements support grpc functions
type Server struct {
	breezrpc.UnimplementedSupportServer
	emailNotifier func(in *breezrpc.ReportPaymentFailureRequest) error
}

func NewServer(emailNotifier func(in *breezrpc.ReportPaymentFailureRequest) error) *Server {
	return &Server{
		emailNotifier: emailNotifier,
	}
}

func (s *Server) ReportPaymentFailure(ctx context.Context, in *breezrpc.ReportPaymentFailureRequest) (*breezrpc.ReportPaymentFailureReply, error) {
	if err := s.emailNotifier(in); err != nil {
		return nil, errors.New("Failed to report payment failure")
	}
	return &breezrpc.ReportPaymentFailureReply{}, nil
}
