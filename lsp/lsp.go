package lsp

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/breez/server/auth"
	"github.com/breez/server/breez"
	lspdrpc "github.com/breez/server/lsp/rpc"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Server implements lsp grpc functions
type Server struct {
	breez.UnimplementedChannelOpenerServer
	breez.UnimplementedPaymentNotifierServer
	DBLSPList func(keys []string) ([]string, error)
}

// lspdLSP represents the infos about a LSP running lspd
type lspdLSP struct {
	Server string
	Token  string
	NoTLS  bool
}

// lnurlLSP represents the infos about a LSP using lnurl
type lnurlLSP struct {
	Name      string
	WidgetURL string
}

type lspConfig struct {
	LspdList  map[string]lspdLSP  `json:"lspd,omitempty"`
	LnurlList map[string]lnurlLSP `json:"lnurl,omitempty"`
}

type lspdClient struct {
	channelOpenerClient lspdrpc.ChannelOpenerClient
	notificationClient  lspdrpc.NotificationsClient
}

var (
	lspConf     lspConfig
	lspdClients map[string]*lspdClient
)

// InitLSP initialize lsp configuration and connections
func InitLSP() error {
	err := readConfig()
	if err != nil {
		return errors.Wrapf(err, "Error in LSP Initialization")
	}
	log.Printf("LSP Configuration: %#v", lspConf)
	systemCertPool, err := x509.SystemCertPool()
	if err != nil {
		return errors.Wrapf(err, "Error getting SystemCertPool in InitLSP")
	}
	creds := credentials.NewClientTLSFromCert(systemCertPool, "")
	lspdClients = make(map[string]*lspdClient, len(lspConf.LspdList))
	for id, LSP := range lspConf.LspdList {
		log.Printf("LSP id: %v; server: %v; token: %v", id, LSP.Server, LSP.Token)
		if LSP.Server != "" {
			var dialOptions []grpc.DialOption
			if LSP.NoTLS {
				dialOptions = append(dialOptions, grpc.WithInsecure())
			} else {
				dialOptions = append(dialOptions, grpc.WithTransportCredentials(creds))
			}
			conn, err := grpc.Dial(LSP.Server, dialOptions...)
			if err != nil {
				log.Printf("Failed to connect to server gRPC: %v", err)
			} else {
				lspdClients[id] = &lspdClient{
					channelOpenerClient: lspdrpc.NewChannelOpenerClient(conn),
					notificationClient:  lspdrpc.NewNotificationsClient(conn),
				}
			}
		}
	}
	return nil
}

func readConfig() error {
	lspConfig := os.Getenv("LSP_CONFIG")
	err := loadConfig(bytes.NewReader([]byte(lspConfig)))
	if err != nil {
		log.Printf("Unable to load the configuration from %s: %v", lspConfig, err)
		return errors.Wrapf(err, "Unable to load the configuration from %s", lspConfig)
	}
	return nil
}

func loadConfig(reader io.Reader) error {
	dec := json.NewDecoder(reader)
	return dec.Decode(&lspConf)
}

// LSPList returns the list of active lsps
func (s *Server) LSPList(ctx context.Context, in *breez.LSPListRequest) (*breez.LSPListReply, error) {
	r := breez.LSPListReply{Lsps: make(map[string]*breez.LSPInformation)}
	keys := auth.GetHeaderKeys(ctx)
	list, err := s.DBLSPList(keys)
	if err != nil {
		log.Printf("Error in DBLSPList(%#v): %v", keys, err)
		return &r, fmt.Errorf("error in DBLSPList(%#v): %w", keys, err)
	}
	for _, id := range list {
		c, ok := lspdClients[id]
		if !ok {
			continue
		}
		clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lspConf.LspdList[id].Token)
		ci, err := c.channelOpenerClient.ChannelInformation(clientCtx, &lspdrpc.ChannelInformationRequest{Pubkey: in.Pubkey})
		if err != nil {
			log.Printf("Error in ChannelInformation for lsdp %v: %v", id, err)
		} else {
			log.Printf("ChannelInformation: %#v", ci)
			var menu []*breez.OpeningFeeParams
			for _, params := range ci.OpeningFeeParamsMenu {
				menu = append(menu, &breez.OpeningFeeParams{
					MinMsat:              params.MinMsat,
					Proportional:         params.Proportional,
					ValidUntil:           params.ValidUntil,
					MaxIdleTime:          params.MaxIdleTime,
					MaxClientToSelfDelay: params.MaxClientToSelfDelay,
					Promise:              params.Promise,
				})
			}
			li := &breez.LSPInformation{
				Name:                  ci.Name,
				Pubkey:                ci.Pubkey,
				Host:                  ci.Host,
				ChannelCapacity:       ci.ChannelCapacity,
				TargetConf:            ci.TargetConf,
				BaseFeeMsat:           ci.BaseFeeMsat,
				FeeRate:               ci.FeeRate,
				TimeLockDelta:         ci.TimeLockDelta,
				MinHtlcMsat:           ci.MinHtlcMsat,
				ChannelFeePermyriad:   ci.ChannelFeePermyriad,
				ChannelMinimumFeeMsat: ci.ChannelMinimumFeeMsat,
				LspPubkey:             ci.LspPubkey,
				MaxInactiveDuration:   ci.MaxInactiveDuration,
				OpeningFeeParamsMenu:  menu,
			}
			r.Lsps[id] = li
		}
	}
	for id, c := range lspConf.LnurlList {
		r.Lsps[id] = &breez.LSPInformation{Name: c.Name, WidgetUrl: c.WidgetURL}
	}
	return &r, nil
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}

	return false
}

// RegisterPayment sends information concerning a payment used by the LSP to open a channel
func (s *Server) RegisterPayment(ctx context.Context, in *breez.RegisterPaymentRequest) (*breez.RegisterPaymentReply, error) {
	lsp, ok := lspConf.LspdList[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.PermissionDenied, "Not authorized")
	}
	keys := auth.GetHeaderKeys(ctx)
	lspList, err := s.DBLSPList(auth.GetHeaderKeys(ctx))
	if err != nil {
		log.Printf("Error in DBLSPList(%#v): %v", keys, err)
		return nil, status.Errorf(codes.PermissionDenied, "Not authorized")
	}
	if !contains(lspList, in.LspId) {
		return nil, status.Errorf(codes.PermissionDenied, "Not authorized")
	}

	lspdClient, ok := lspdClients[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lsp.Token)
	_, err = lspdClient.channelOpenerClient.RegisterPayment(clientCtx, &lspdrpc.RegisterPaymentRequest{Blob: in.Blob})
	if err != nil {
		return nil, err
	}
	return &breez.RegisterPaymentReply{}, nil
}

// CheckChannels call lspd function CheckChannels
func (s *Server) CheckChannels(ctx context.Context, in *breez.CheckChannelsRequest) (*breez.CheckChannelsReply, error) {
	lsp, ok := lspConf.LspdList[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	lspdClient, ok := lspdClients[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lsp.Token)
	reply, err := lspdClient.channelOpenerClient.CheckChannels(clientCtx, &lspdrpc.Encrypted{Data: in.Blob})
	if err != nil {
		return nil, err
	}
	return &breez.CheckChannelsReply{Blob: reply.Data}, nil
}

func (s *Server) RegisterPaymentNotification(
	ctx context.Context,
	in *breez.RegisterPaymentNotificationRequest,
) (*breez.RegisterPaymentNotificationResponse, error) {
	lsp, ok := lspConf.LspdList[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	lspdClient, ok := lspdClients[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lsp.Token)
	_, err := lspdClient.notificationClient.SubscribeNotifications(clientCtx, &lspdrpc.EncryptedNotificationRequest{
		Blob: in.Blob,
	})
	if err != nil {
		return nil, err
	}
	return &breez.RegisterPaymentNotificationResponse{}, nil
}
