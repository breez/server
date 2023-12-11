package support

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/breez/server/auth"
	breezrpc "github.com/breez/server/breez"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements support grpc functions
type Server struct {
	breezrpc.UnimplementedSupportServer
	emailNotifier func(in *breezrpc.ReportPaymentFailureRequest, keys []string) error
	getStatus     func() (string, error)
	DBLSPList     func(keys []string) ([]string, error)
}

func NewServer(emailNotifier func(in *breezrpc.ReportPaymentFailureRequest, keys []string) error,
	getStatus func() (string, error),
	DBLSPList func(keys []string) ([]string, error)) *Server {
	return &Server{
		emailNotifier: emailNotifier,
		getStatus:     getStatus,
		DBLSPList:     DBLSPList,
	}
}

func (s *Server) ReportPaymentFailure(ctx context.Context, in *breezrpc.ReportPaymentFailureRequest) (*breezrpc.ReportPaymentFailureReply, error) {
	keys, err := s.validateRequest(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.emailNotifier(in, keys); err != nil {
		return nil, errors.New("failed to report payment failure")
	}
	return &breezrpc.ReportPaymentFailureReply{}, nil
}

func (s *Server) BreezStatus(ctx context.Context, in *breezrpc.BreezStatusRequest) (*breezrpc.BreezStatusReply, error) {
	if _, err := s.validateRequest(ctx); err != nil {
		return nil, err
	}
	status, err := s.getStatus()
	if err != nil {
		log.Printf("s.getStatus() error: %v", err)
		return nil, fmt.Errorf("breezstatus error")
	}
	log.Printf("BreezStatus: %v", status)
	return &breezrpc.BreezStatusReply{
		Status: breezrpc.BreezStatusReply_BreezStatus(breezrpc.BreezStatusReply_BreezStatus_value[status]),
	}, nil
}

func (s *Server) validateRequest(ctx context.Context) ([]string, error) {
	keys := auth.GetHeaderKeys(ctx)
	list, err := s.DBLSPList(keys)
	if err != nil {
		log.Printf("Error in DBLSPList(%#v): %v", keys, err)
		return []string{}, status.Errorf(codes.PermissionDenied, "Not authorized")
	}
	if len(list) == 0 {
		log.Printf("No lsps found: %#v", keys)
		return []string{}, status.Errorf(codes.PermissionDenied, "Not authorized")
	}
	return keys, nil
}
