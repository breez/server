package lsp

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"os"

	lspdrpc "github.com/breez/lspd/rpc"
	"github.com/breez/server/breez"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Server implements lsp grpc functions
type Server struct {
	EmailNotifier func(nid, txid string, index uint32) error
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

var (
	lspConf     lspConfig
	lspdClients map[string]lspdrpc.ChannelOpenerClient
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
	lspdClients = make(map[string]lspdrpc.ChannelOpenerClient, len(lspConf.LspdList))
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
				lspdClients[id] = lspdrpc.NewChannelOpenerClient(conn)
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
	for id, c := range lspdClients {
		clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lspConf.LspdList[id].Token)
		ci, err := c.ChannelInformation(clientCtx, &lspdrpc.ChannelInformationRequest{Pubkey: in.Pubkey})
		if err != nil {
			log.Printf("Error in ChannelInformation for lsdp %v: %v", id, err)
		} else {
			li := &breez.LSPInformation{
				Name:            ci.Name,
				Pubkey:          ci.Pubkey,
				Host:            ci.Host,
				ChannelCapacity: ci.ChannelCapacity,
				TargetConf:      ci.TargetConf,
				BaseFeeMsat:     ci.BaseFeeMsat,
				FeeRate:         ci.FeeRate,
				TimeLockDelta:   ci.TimeLockDelta,
				MinHtlcMsat:     ci.MinHtlcMsat,
				LspPubkey:       ci.LspPubkey,
			}
			log.Printf("Got lsp information: %v", li)
			r.Lsps[id] = li
		}
	}
	for id, c := range lspConf.LnurlList {
		r.Lsps[id] = &breez.LSPInformation{Name: c.Name, WidgetUrl: c.WidgetURL}
	}
	return &r, nil
}

// OpenLSPChannel call OpenChannel of the lspd given by it's id
func (s *Server) OpenLSPChannel(ctx context.Context, in *breez.OpenLSPChannelRequest) (*breez.OpenLSPChannelReply, error) {
	lsp, ok := lspConf.LspdList[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	lspdClient, ok := lspdClients[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lsp.Token)
	r, err := lspdClient.OpenChannel(clientCtx, &lspdrpc.OpenChannelRequest{Pubkey: in.Pubkey})
	if err != nil {
		return nil, err // Log and returns another error.
	}
	if s.EmailNotifier != nil {
		_ = s.EmailNotifier(in.Pubkey, r.TxHash, r.OutputIndex)
	}
	return &breez.OpenLSPChannelReply{}, nil
}

// RegisterPayment sends information concerning a payment used by the LSP to open a channel
func (s *Server) RegisterPayment(ctx context.Context, in *breez.RegisterPaymentRequest) (*breez.RegisterPaymentReply, error) {
	lsp, ok := lspConf.LspdList[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	lspdClient, ok := lspdClients[in.LspId]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Not found")
	}
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lsp.Token)
	_, err := lspdClient.RegisterPayment(clientCtx, &lspdrpc.RegisterPaymentRequest{Blob: in.Blob})
	if err != nil {
		return nil, err
	}
	return &breez.RegisterPaymentReply{}, nil
}
