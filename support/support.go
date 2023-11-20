package support

import (
	"context"
	"errors"
	"fmt"
	"log"

	breezrpc "github.com/breez/server/breez"
)

// Server implements support grpc functions
type Server struct {
	breezrpc.UnimplementedSupportServer
	emailNotifier func(in *breezrpc.ReportPaymentFailureRequest) error
	getStatus     func() (string, error)
}

func NewServer(emailNotifier func(in *breezrpc.ReportPaymentFailureRequest) error,
	getStatus func() (string, error)) *Server {
	return &Server{
		emailNotifier: emailNotifier,
		getStatus:     getStatus,
	}
}

func (s *Server) ReportPaymentFailure(ctx context.Context, in *breezrpc.ReportPaymentFailureRequest) (*breezrpc.ReportPaymentFailureReply, error) {
	if err := s.emailNotifier(in); err != nil {
		return nil, errors.New("failed to report payment failure")
	}
	return &breezrpc.ReportPaymentFailureReply{}, nil
}

func (s *Server) BreezStatus(ctx context.Context, in *breezrpc.BreezStatusRequest) (*breezrpc.BreezStatusReply, error) {
	status, err := s.getStatus()
	if err != nil {
		log.Printf("s.getStatus() error: %v", err)
		return nil, fmt.Errorf("breezstatus eror")
	}
	return &breezrpc.BreezStatusReply{
		Status: breezrpc.BreezStatusReply_BreezStatus(breezrpc.BreezStatusReply_BreezStatus_value[status]),
	}, nil
}
