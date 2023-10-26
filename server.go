package main

// To generate breez/breez.pb.go run:
// protoc -I breez breez/breez.proto --go_out=plugins=grpc:breez

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/breez/server/auth"
	"github.com/breez/server/breez"
	"github.com/breez/server/captcha"
	"github.com/breez/server/lsp"
	"github.com/breez/server/ratelimit"
	"github.com/breez/server/signer"
	"github.com/breez/server/swapper"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/chainrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/lightningnetwork/lnd/lnrpc/submarineswaprpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"

	"golang.org/x/sync/singleflight"
	"golang.org/x/text/message"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

const (
	imageDimensionLength = 200
	channelAmount        = 1000000
	minRemoveFund        = 50000
)

var client, ssClient lnrpc.LightningClient
var subswapClient submarineswaprpc.SubmarineSwapperClient
var signerClient signrpc.SignerClient
var walletKitClient, ssWalletKitClient walletrpc.WalletKitClient
var chainNotifierClient chainrpc.ChainNotifierClient
var ssRouterClient routerrpc.RouterClient
var network *chaincfg.Params
var openChannelReqGroup singleflight.Group

var swapperServer *swapper.Server

// server is used to implement breez.InvoicerServer and breez.PosServer
type server struct {
	breez.UnimplementedInvoicerServer
	breez.UnimplementedPosServer
	breez.UnimplementedInformationServer
	breez.UnimplementedCardOrdererServer
	breez.UnimplementedFundManagerServer
	breez.UnimplementedCTPServer
	breez.UnimplementedSyncNotifierServer
	breez.UnimplementedPushTxNotifierServer
	breez.UnimplementedInactiveNotifierServer
	breez.UnimplementedNodeInfoServer
}

// RegisterDevice implements breez.InvoicerServer
func (s *server) RegisterDevice(ctx context.Context, in *breez.RegisterRequest) (*breez.RegisterReply, error) {
	if in.LightningID != "" {
		nodeID, err := hex.DecodeString(in.LightningID)
		if err != nil {
			log.Printf("hex.DecodeString(%v) error: %v", in.LightningID, err)
			return nil, fmt.Errorf("hex.DecodeString(%v) error: %w", in.LightningID, err)
		}
		err = deviceNode(nodeID, in.DeviceID)
		if err != nil {
			log.Printf("deviceNode(%x, %v) error: %v", nodeID, in.DeviceID, err)
			return nil, fmt.Errorf("deviceNode(%x, %v) error: %w", nodeID, in.DeviceID, err)
		}
	}
	return &breez.RegisterReply{BreezID: in.DeviceID}, nil
}

func (s *server) SendInvoice(ctx context.Context, in *breez.PaymentRequest) (*breez.InvoiceReply, error) {

	notificationData := map[string]string{
		"msg":             "Payment request",
		"payee":           in.Payee,
		"amount":          strconv.FormatInt(in.Amount, 10),
		"payment_request": in.Invoice,
	}

	err := notifyAlertMessage(
		in.Payee,
		"is requesting you to pay "+strconv.FormatInt(in.Amount, 10)+" Sat",
		notificationData,
		in.BreezID)

	if err != nil {
		log.Println(err)
		return &breez.InvoiceReply{Error: err.Error()}, err
	}

	return &breez.InvoiceReply{Error: ""}, nil
}

func (s *server) UploadLogo(ctx context.Context, in *breez.UploadFileRequest) (*breez.UploadFileReply, error) {

	//validate png image
	fileDataReader := bytes.NewReader(in.Content)
	img, err := png.Decode(fileDataReader)
	if err != nil {
		log.Println("Failes to decode image", err)
		return nil, status.Errorf(codes.InvalidArgument, "Image must be of type png")
	}

	//validate image size
	imageMaxBounds := img.Bounds().Max
	if imageMaxBounds.X != imageDimensionLength || imageMaxBounds.Y != imageDimensionLength {
		log.Println("Image not in right required dimensions", imageMaxBounds)
		return nil, status.Errorf(codes.InvalidArgument, "Image size must be 200 X 200 pixels")
	}

	//hash content and calculate file path
	fileHash := sha256.Sum256(in.Content)
	hashHex := hex.EncodeToString(fileHash[0:])
	objectPath := fmt.Sprintf("%v/%v/%v.png", hashHex[:4], hashHex[4:8], hashHex[8:])

	gcContext := context.Background()
	gcCredsFile := os.Getenv("GOOGLE_CLOUD_SERVICE_FILE")
	gcBucketName := os.Getenv("GOOGLE_CLOUD_IMAGES_BUCKET_NAME")
	gcClient, err := storage.NewClient(gcContext, option.WithCredentialsFile(gcCredsFile))

	if err != nil {
		log.Println("Failed to create google cloud client", err)
		return nil, status.Errorf(codes.Internal, "Failed to save image")
	}
	bucket := gcClient.Bucket(gcBucketName)
	obj := bucket.Object(objectPath)

	writer := obj.NewWriter(gcContext)
	if _, err := writer.Write(in.Content); err != nil {
		log.Println("Failed to write to bucket stream", err)
		return nil, status.Errorf(codes.Internal, "Failed to save image")
	}

	if err := writer.Close(); err != nil {
		log.Println("Failed to close bucket stream", err)
		return nil, status.Errorf(codes.Internal, "Failed to save image")
	}

	if err := obj.ACL().Set(gcContext, storage.AllUsers, storage.RoleReader); err != nil {
		log.Println("Failed to set read permissions on object", err)
		return nil, status.Errorf(codes.Internal, "Failed to save image")
	}

	objAttrs, err := obj.Attrs(gcContext)
	if err != nil {
		log.Println("Failed read object attributes", err)
		return nil, status.Errorf(codes.Internal, "Failed to save image")
	}

	log.Println("Succesfully uploaded image", objAttrs.MediaLink)
	return &breez.UploadFileReply{Url: objAttrs.MediaLink}, nil
}

// Workaround until LND PR #1595 is merged
func (s *server) UpdateChannelPolicy(ctx context.Context, in *breez.UpdateChannelPolicyRequest) (*breez.UpdateChannelPolicyReply, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	nodeChannels, err := getNodeChannels(in.PubKey)
	if err != nil {
		return nil, err
	}

	for _, c := range nodeChannels {
		var channelPoint lnrpc.ChannelPoint

		outputIndex, err := strconv.ParseUint(strings.Split(c.ChannelPoint, ":")[1], 10, 32)
		if err != nil {
			return nil, err
		}

		channelPoint.OutputIndex = uint32(outputIndex)
		channelPoint.FundingTxid = &lnrpc.ChannelPoint_FundingTxidStr{FundingTxidStr: strings.Split(c.ChannelPoint, ":")[0]}

		client.UpdateChannelPolicy(clientCtx, &lnrpc.PolicyUpdateRequest{BaseFeeMsat: 1000, FeeRate: 0.000001, TimeLockDelta: 144, Scope: &lnrpc.PolicyUpdateRequest_ChanPoint{ChanPoint: &channelPoint}})
		if err != nil {
			return nil, err
		}
	}

	return &breez.UpdateChannelPolicyReply{}, nil
}

func (s *server) OpenChannel(ctx context.Context, in *breez.OpenChannelRequest) (*breez.OpenChannelReply, error) {
	return nil, fmt.Errorf("disabled")
}

func (s *server) AddFundInit(ctx context.Context, in *breez.AddFundInitRequest) (*breez.AddFundInitReply, error) {
	return swapperServer.AddFundInitLegacy(ctx, in)
}

func (s *server) AddFundStatus(ctx context.Context, in *breez.AddFundStatusRequest) (*breez.AddFundStatusReply, error) {
	return swapperServer.AddFundStatus(ctx, in)
}

func (s *server) GetSwapPayment(ctx context.Context, in *breez.GetSwapPaymentRequest) (*breez.GetSwapPaymentReply, error) {
	return swapperServer.GetSwapPaymentLegacy(ctx, in)
}

func (s *server) RemoveFund(ctx context.Context, in *breez.RemoveFundRequest) (*breez.RemoveFundReply, error) {
	address := in.Address
	amount := in.Amount
	if address == "" {
		return nil, errors.New("Destination address must not be empty")
	}

	_, err := btcutil.DecodeAddress(address, network)
	if err != nil {
		log.Println("Destination address must be a valid bitcoin address")
		return nil, err
	}

	if amount <= 0 {
		return nil, errors.New("Amount must be positive")
	}

	if amount < minRemoveFund {
		p := message.NewPrinter(message.MatchLanguage("en"))
		satFormatted := strings.Replace(p.Sprintf("%d", minRemoveFund), ",", " ", 1)
		btcFormatted := strconv.FormatFloat(float64(minRemoveFund)/float64(100000000), 'f', -1, 64)
		errorStr := fmt.Sprintf("Removed funds must be more than  %v BTC (%v Sat).", btcFormatted, satFormatted)
		return &breez.RemoveFundReply{ErrorMessage: errorStr}, nil
	}

	paymentRequest, err := createRemoveFundPaymentRequest(amount, address)
	if err != nil {
		log.Printf("createRemoveFundPaymentRequest: failed %v", err)
		return nil, err
	}

	return &breez.RemoveFundReply{PaymentRequest: paymentRequest}, nil
}

func (s *server) RedeemRemovedFunds(ctx context.Context, in *breez.RedeemRemovedFundsRequest) (*breez.RedeemRemovedFundsReply, error) {
	txID, err := ensureOnChainPaymentSent(in.Paymenthash)
	if err != nil {
		log.Printf("ReceiveOnChainPayment failed: %v", err)
		return nil, err
	}
	return &breez.RedeemRemovedFundsReply{Txid: txID}, nil
}

// RegisterDevice implements breez.InvoicerServer
func (s *server) Order(ctx context.Context, in *breez.OrderRequest) (*breez.OrderReply, error) {
	log.Printf("Order a card for: %#v", *in)
	err := sendCardOrderNotification(in)
	if err != nil {
		log.Printf("Error in sendCardOrderNotification: %v", err)
	}
	return &breez.OrderReply{}, nil
}

// InactiveNotify send a notification to an inactive nodeid
func (s *server) InactiveNotify(ctx context.Context, in *breez.InactiveNotifyRequest) (*breez.InactiveNotifyResponse, error) {
	data := make(map[string]string)
	token, err := getDeviceToken(in.Pubkey)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, fmt.Errorf("Unknown nodeID: %v", in.Pubkey)
	}
	body := fmt.Sprintf("You haven't made any payments with Breez for %v days, so your LSP might have to close your channels. Open Breez for more information.", in.Days)
	err = notifyAlertMessage("Inactive Channels", body, data, token)
	if err != nil {
		return nil, err
	}
	return &breez.InactiveNotifyResponse{}, nil
}

// JoinCTPSession is used by both payer/payee to join a CTP session.
func (s *server) JoinCTPSession(ctx context.Context, in *breez.JoinCTPSessionRequest) (*breez.JoinCTPSessionResponse, error) {
	sessionID, expiry, err := joinSession(in.SessionID, in.NotificationToken, in.PartyName, in.PartyType == breez.JoinCTPSessionRequest_PAYER)
	if err != nil {
		return nil, err
	}
	return &breez.JoinCTPSessionResponse{SessionID: sessionID, Expiry: expiry}, nil
}

func (s *server) TerminateCTPSession(ctx context.Context, in *breez.TerminateCTPSessionRequest) (*breez.TerminateCTPSessionResponse, error) {
	err := terminateSession(in.SessionID)
	if err != nil {
		return nil, err
	}
	return &breez.TerminateCTPSessionResponse{}, nil
}

func (s *server) RegisterTransactionConfirmation(ctx context.Context, in *breez.RegisterTransactionConfirmationRequest) (*breez.RegisterTransactionConfirmationResponse, error) {
	var notifyType string
	if in.NotificationType == breez.RegisterTransactionConfirmationRequest_READY_RECEIVE_PAYMENT {
		notifyType = receivePaymentType
	}
	if in.NotificationType == breez.RegisterTransactionConfirmationRequest_CHANNEL_OPENED {
		notifyType = channelOpenedType
	}
	if notifyType == "" {
		return nil, errors.New("Invalid notification type")
	}
	err := registerTransacionConfirmation(in.TxID, in.NotificationToken, notifyType)
	if err != nil {
		return nil, err
	}
	return &breez.RegisterTransactionConfirmationResponse{}, nil
}

func (s *server) RegisterPeriodicSync(ctx context.Context, in *breez.RegisterPeriodicSyncRequest) (*breez.RegisterPeriodicSyncResponse, error) {
	if err := registerSyncNotification(in.NotificationToken); err != nil {
		return nil, err
	}
	return &breez.RegisterPeriodicSyncResponse{}, nil
}

func getNodeChannels(nodeID string) ([]*lnrpc.Channel, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	listResponse, err := client.ListChannels(clientCtx, &lnrpc.ListChannelsRequest{})
	if err != nil {
		return nil, err
	}
	var nodeChannels []*lnrpc.Channel
	for _, channel := range listResponse.Channels {
		if channel.RemotePubkey == nodeID {
			nodeChannels = append(nodeChannels, channel)
		}
	}
	return nodeChannels, nil
}

func getPendingNodeChannels(nodeID string) ([]*lnrpc.PendingChannelsResponse_PendingOpenChannel, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	pendingResponse, err := client.PendingChannels(clientCtx, &lnrpc.PendingChannelsRequest{})
	if err != nil {
		return nil, err
	}
	var pendingChannels []*lnrpc.PendingChannelsResponse_PendingOpenChannel
	for _, p := range pendingResponse.PendingOpenChannels {
		if p.Channel.RemoteNodePub == nodeID {
			pendingChannels = append(pendingChannels, p)
		}
	}
	return pendingChannels, nil
}

func main() {

	switch os.Getenv("NETWORK") {
	case "simnet":
		network = &chaincfg.SimNetParams
	case "testnet":
		network = &chaincfg.TestNet3Params
	default:
		network = &chaincfg.MainNetParams
	}

	lisGRPC, err := net.Listen("tcp", os.Getenv("GRPC_LISTEN_ADDRESS"))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	lisHTTP, err := net.Listen("tcp", os.Getenv("HTTP_LISTEN_ADDRESS"))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/fees/v1/btc-fee-estimates.json", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(feeEstimates))
	})
	HTTPServer := &http.Server{
		Handler:        mux,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	go HTTPServer.Serve(lisHTTP)

	// Creds file to connect to LND gRPC
	cp := x509.NewCertPool()
	if !cp.AppendCertsFromPEM([]byte(strings.Replace(os.Getenv("LND_CERT"), "\\n", "\n", -1))) {
		log.Fatalf("credentials: failed to append certificates")
	}
	creds := credentials.NewClientTLSFromCert(cp, "")

	// Address of an LND instance
	conn, err := grpc.Dial(os.Getenv("LND_ADDRESS"), grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Fatalf("Failed to connect to LND gRPC: %v", err)
	}
	defer conn.Close()
	client = lnrpc.NewLightningClient(conn)
	signerClient = signrpc.NewSignerClient(conn)
	walletKitClient = walletrpc.NewWalletKitClient(conn)
	chainNotifierClient = chainrpc.NewChainNotifierClient(conn)

	ssCp := x509.NewCertPool()
	if !ssCp.AppendCertsFromPEM([]byte(strings.Replace(os.Getenv("SUBSWAPPER_LND_CERT"), "\\n", "\n", -1))) {
		log.Fatalf("credentials: failed to append certificates")
	}
	ssCreds := credentials.NewClientTLSFromCert(ssCp, "")
	// Address of an LND instance
	subswapConn, err := grpc.Dial(os.Getenv("SUBSWAPPER_LND_ADDRESS"), grpc.WithTransportCredentials(ssCreds))
	if err != nil {
		log.Fatalf("Failed to connect to LND gRPC: %v", err)
	}
	defer subswapConn.Close()
	ssClient = lnrpc.NewLightningClient(subswapConn)
	subswapClient = submarineswaprpc.NewSubmarineSwapperClient(subswapConn)
	ssWalletKitClient = walletrpc.NewWalletKitClient(subswapConn)
	ssRouterClient = routerrpc.NewRouterClient(subswapConn)

	ctx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	go subscribeTransactions(ctx, client)
	go handlePastTransactions(ctx, client)
	go subscribeChannelAcceptor(ctx, client, os.Getenv("LND_CHANNEL_ACCEPTOR"))

	ssCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("SUBSWAPPER_LND_MACAROON_HEX"))
	go subscribeTransactions(ssCtx, ssClient)
	go handlePastTransactions(ssCtx, ssClient)
	go subscribeChannelAcceptor(ssCtx, ssClient, os.Getenv("SUBSWAPPER_LND_CHANNEL_ACCEPTOR"))

	startFeeEstimates()

	err = redisConnect()
	if err != nil {
		log.Println("redisConnect error:", err)
	}
	go deliverSyncNotifications()

	err = pgConnect()
	if err != nil {
		log.Printf("pgConnect error: %v", err)
	}
	go registerPastBoltzReverseSwapTxNotifications()

	lsp.InitLSP()

	s := grpc.NewServer(
		grpc_middleware.WithUnaryServerChain(
			auth.UnaryMultiAuth("/breez.PublicChannelOpener/", os.Getenv("PUBLIC_CHANNEL_TOKENS")),
			auth.UnaryAuth("/breez.InactiveNotifier/", os.Getenv("INACTIVE_NOTIFIER_TOKEN")),
			captcha.UnaryCaptchaAuth("/breez.ChannelOpener/OpenLSPChannel", os.Getenv("CAPTCHA_CONFIG")),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/RegisterDevice", 3, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/RegisterDevice", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/SendInvoice", 3, 100, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/SendInvoice", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.CardOrderer/Order", 3, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.CardOrderer/Order", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/RegisterDevice", 10, 20, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/RegisterDevice", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/UploadLogo", 100, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/UploadLogo", 10000, 100000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Ping", 10000, 100000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Ping", 100000, 10000000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Rates", 10000, 100000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Rates", 100000, 10000000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/ReceiverInfo", 10000, 100000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/ReceiverInfo", 100000, 10000000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/OpenChannel", 5, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/OpenChannel", 500, 1000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/UpdateChannelPolicy", 1000, 100000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/UpdateChannelPolicy", 100000, 1000000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/AddFundInit", 20, 200, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/AddFundInit", 1000, 100000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/AddFundStatus", 100, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/AddFundStatus", 1000, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/RemoveFund", 3, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/RemoveFund", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/RedeemRemovedFunds", 10, 100, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/RedeemRemovedFunds", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/GetSwapPayment", 100, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/GetSwapPayment", 1000, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/RegisterTransactionConfirmation", 10, 100, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.FundManager/RegisterTransactionConfirmation", 100, 10000, 86400),

			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/AddFundInit", 20, 200, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/AddFundInit", 1000, 100000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/AddFundStatus", 100, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/AddFundStatus", 1000, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/GetSwapPayment", 100, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/GetSwapPayment", 1000, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/GetReverseRoutingNode", 100, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Swapper/GetReverseRoutingNode", 1000, 10000, 86400),

			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/JoinCTPSession", 1000, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/JoinCTPSession", 1000, 100000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/TerminateCTPSession", 1000, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/TerminateCTPSession", 1000, 100000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.ChannelOpener/LSPList", 10000, 10000000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.ChannelOpener/LSPList", 10000, 10000000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.ChannelOpener/OpenLSPChannel", 10, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.ChannelOpener/OpenLSPChannel", 1000, 1000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.PushTxNotifier/RegisterTxNotification", 10, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.PushTxNotifier/RegisterTxNotification", 1000, 1000, 86400),

			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez/PublicChannelOpener/OpenPublicChannel", 10, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez/PublicChannelOpener/OpenPublicChannel", 1000, 1000, 86400),

			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez/InactiveNotifier/InactiveNotify", 1000, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez/InactiveNotifier/InactiveNotify", 1000, 1000, 86400),

			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.NodeInfo/SetNodeInfo", 1000, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.NodeInfo/SetNodeInfo", 1000, 100000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.NodeInfo/GetNodeInfo", 1000, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.NodeInfo/GetNodeInfo", 1000, 100000, 86400),

			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Signer/SignUrl", 10, 10000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Signer/SignUrl", 1000, 1000, 86400),
		),
	)

	swapperServer = swapper.NewServer(network, redisPool, client, ssClient, subswapClient, ssWalletKitClient, ssRouterClient,
		insertSubswapPayment, updateSubswapPayment, hasFilteredAddress)
	breez.RegisterSwapperServer(s, swapperServer)

	lspServer := &lsp.Server{
		EmailNotifier: sendOpenChannelNotification,
		DBLSPList:     lspList,
	}
	breez.RegisterChannelOpenerServer(s, lspServer)
	breez.RegisterPublicChannelOpenerServer(s, lspServer)
	breez.RegisterInvoicerServer(s, &server{})
	breez.RegisterPosServer(s, &server{})
	breez.RegisterInformationServer(s, &server{})
	breez.RegisterCardOrdererServer(s, &server{})
	breez.RegisterFundManagerServer(s, &server{})
	breez.RegisterCTPServer(s, &server{})
	breez.RegisterSyncNotifierServer(s, &server{})
	breez.RegisterPushTxNotifierServer(s, &server{})
	breez.RegisterInactiveNotifierServer(s, &server{})
	breez.RegisterNodeInfoServer(s, &server{})
	breez.RegisterSignerServer(s, &signer.Server{})

	// Register reflection service on gRPC server.
	reflection.Register(s)
	if err := s.Serve(lisGRPC); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
