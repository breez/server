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

// LSP represents the infos about a LSP
type LSP struct {
	Server string
	Token  string
}

var (
	lspList     map[string]LSP
	lspdClients map[string]lspdrpc.ChannelOpenerClient
)

// InitLSP initialize lsp configuration and connections
func InitLSP() error {
	err := readConfig()
	if err != nil {
		return errors.Wrapf(err, "Error in LSP Initialization")
	}
	systemCertPool, err := x509.SystemCertPool()
	if err != nil {
		return errors.Wrapf(err, "Error getting SystemCertPool in InitLSP")
	}
	creds := credentials.NewClientTLSFromCert(systemCertPool, "")
	lspdClients = make(map[string]lspdrpc.ChannelOpenerClient, len(lspList))

	for id, LSP := range lspList {
		if LSP.Server != "" {
			conn, err := grpc.Dial(LSP.Server, grpc.WithTransportCredentials(creds))
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
		return errors.Wrapf(err, "Unable to load the configuration from %s", lspConfig)
	}
	return nil
}

func loadConfig(reader io.Reader) error {
	dec := json.NewDecoder(reader)
	return dec.Decode(&lspList)
}

// LSPList returns the list of active lsps
func (s *Server) LSPList(ctx context.Context, in *breez.LSPListRequest) (*breez.LSPListReply, error) {
	r := breez.LSPListReply{Lsps: make(map[string]*breez.LSPInformation)}
	for id, c := range lspdClients {
		clientCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+lspList[id].Token)
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
			}
			r.Lsps[id] = li
		}
	}
	return &r, nil
}

// OpenLSPChannel call OpenChannel of the lspd given by it's id
func (s *Server) OpenLSPChannel(ctx context.Context, in *breez.OpenLSPChannelRequest) (*breez.OpenLSPChannelReply, error) {
	lsp, ok := lspList[in.LspId]
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
