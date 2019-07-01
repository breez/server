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
	"os"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/breez/server/breez"
	"github.com/breez/server/ratelimit"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcutil"
	"github.com/gomodule/redigo/redis"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/submarineswaprpc"
	"github.com/lightningnetwork/lnd/zpay32"

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
	imageDimensionLength    = 200
	channelAmount           = 1000000
	depositBalanceThreshold = 900000
	minRemoveFund           = 50000
	chanReserve             = 600
)

var client lnrpc.LightningClient
var subswapClient submarineswaprpc.SubmarineSwapperClient
var network *chaincfg.Params
var openChannelReqGroup singleflight.Group

// server is used to implement breez.InvoicerServer and breez.PosServer
type server struct{}

// RegisterDevice implements breez.InvoicerServer
func (s *server) RegisterDevice(ctx context.Context, in *breez.RegisterRequest) (*breez.RegisterReply, error) {
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

	r, err, _ := openChannelReqGroup.Do(in.PubKey, func() (interface{}, error) {
		clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
		nodeChannels, err := getNodeChannels(in.PubKey)
		if err != nil {
			return nil, err
		}
		pendingChannels, err := getPendingNodeChannels(in.PubKey)
		if err != nil {
			return nil, err
		}
		if len(nodeChannels) == 0 && len(pendingChannels) == 0 {
			response, err := client.OpenChannelSync(clientCtx, &lnrpc.OpenChannelRequest{
				LocalFundingAmount: channelAmount,
				NodePubkeyString:   in.PubKey,
				PushSat:            0,
				TargetConf:         1,
				MinHtlcMsat:        600,
				Private:            true,
			})
			log.Printf("Response from OpenChannel: %#v (TX: %v)", response, hex.EncodeToString(response.GetFundingTxidBytes()))

			if err != nil {
				log.Printf("Error in OpenChannel: %v", err)
				return nil, err
			}
			var txidStr string
			txid, err := chainhash.NewHash(response.GetFundingTxidBytes())

			// don't fail the request in case we can't format the channel id from
			// some reason...
			if txid != nil {
				txidStr = txid.String()
			}
			_ = sendOpenChannelNotification(in.PubKey, txidStr, response.GetOutputIndex())
		}
		return &breez.OpenChannelReply{}, nil
	})

	if err != nil {
		return nil, err
	}
	return r.(*breez.OpenChannelReply), err
}

func (s *server) AddFundInit(ctx context.Context, in *breez.AddFundInitRequest) (*breez.AddFundInitReply, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))

	maxAllowedDeposit, err := getMaxAllowedDeposit(in.NodeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to calculate max allowed deposit amount")
	}

	if maxAllowedDeposit == 0 {
		p := message.NewPrinter(message.MatchLanguage("en"))
		satFormatted := strings.Replace(p.Sprintf("%d", depositBalanceThreshold), ",", " ", 1)
		btcFormatted := strconv.FormatFloat(float64(depositBalanceThreshold)/float64(100000000), 'f', -1, 64)
		return &breez.AddFundInitReply{
			MaxAllowedDeposit: maxAllowedDeposit,
			ErrorMessage: fmt.Sprintf("Adding funds is enabled when the balance is under %v BTC (%v Sat).",
				btcFormatted, satFormatted),
			RequiredReserve: chanReserve,
		}, nil
	}

	subSwapServiceInitResponse, err := subswapClient.SubSwapServiceInit(clientCtx, &submarineswaprpc.SubSwapServiceInitRequest{
		Hash:   in.Hash,
		Pubkey: in.Pubkey,
	})
	if err != nil {
		log.Printf("subswapClient.SubSwapServiceInit (hash:%v, pubkey:%v) error: %v", in.Hash, in.Pubkey, err)
		return nil, err
	}

	address := subSwapServiceInitResponse.Address
	redisConn := redisPool.Get()
	defer redisConn.Close()
	_, err = redisConn.Do("HMSET", "input-address:"+address, "hash", in.Hash)
	if err != nil {
		return nil, err
	}
	_, err = redisConn.Do("SADD", "input-address-notification:"+address, in.NotificationToken)
	if err != nil {
		return nil, err
	}
	_, err = redisConn.Do("SADD", "fund-addresses", address)
	if err != nil {
		return nil, err
	}
	return &breez.AddFundInitReply{
		Address:           address,
		MaxAllowedDeposit: maxAllowedDeposit,
		Pubkey:            subSwapServiceInitResponse.Pubkey,
		LockHeight:        subSwapServiceInitResponse.LockHeight,
		RequiredReserve:   chanReserve,
	}, nil
}

func (s *server) AddFundStatus(ctx context.Context, in *breez.AddFundStatusRequest) (*breez.AddFundStatusReply, error) {
	statuses := make(map[string]*breez.AddFundStatusReply_AddressStatus)
	redisConn := redisPool.Get()
	defer redisConn.Close()
	for _, address := range in.Addresses {
		m, err := redis.StringMap(redisConn.Do("HGETALL", "input-address:"+address))
		if err != nil {
			log.Println("AddFundStatus error:", err)
			continue
		}
		s := &breez.AddFundStatusReply_AddressStatus{}
		if tx, confirmed := m["tx:TxHash"]; confirmed {
			s.BlockHash = m["tx:BlockHash"]
			s.Confirmed = true
			s.Tx = tx
			if amt, err := strconv.ParseInt(m["tx:Amount"], 10, 64); err == nil {
				s.Amount = amt
			}
		} else {
			if tx, unconfirmed := m["utx:TxHash"]; unconfirmed {
				s.Confirmed = false
				s.Tx = tx
				if amt, err := strconv.ParseInt(m["utx:Amount"], 10, 64); err == nil {
					s.Amount = amt
				}
			}
		}
		_, err = redisConn.Do("SADD", "input-address-notification:"+address, in.NotificationToken)
		if err != nil {
			log.Println("AddFundStatus error adding token:", "input-address-notification:"+address, in.NotificationToken, err)
		}
		if s.Tx != "" {
			statuses[address] = s
		}
	}

	return &breez.AddFundStatusReply{Statuses: statuses}, nil
}

func (s *server) GetSwapPayment(ctx context.Context, in *breez.GetSwapPaymentRequest) (*breez.GetSwapPaymentReply, error) {
	// Decode the the client's payment request
	decodedPayReq, err := zpay32.Decode(in.PaymentRequest, network)
	if err != nil {
		log.Printf("GetSwapPayment - Error in zpay32.Decode: %v", err)
		return nil, status.Errorf(codes.Internal, "payment request is not valid")
	}

	decodedAmt := int64(0)
	if decodedPayReq.MilliSat != nil {
		decodedAmt = int64(decodedPayReq.MilliSat.ToSatoshis())
	}

	maxAllowedDeposit, err := getMaxAllowedDeposit(hex.EncodeToString(decodedPayReq.Destination.SerializeCompressed()))
	if err != nil {
		log.Printf("GetSwapPayment - getMaxAllowedDeposit error: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to calculate max allowed deposit amount")
	}
	if decodedAmt > maxAllowedDeposit {
		log.Printf("GetSwapPayment - decodedAmt > maxAllowedDeposit: %v > %v", decodedAmt, maxAllowedDeposit)
		return &breez.GetSwapPaymentReply{
			FundsExceededLimit: true,
			PaymentError:       fmt.Sprintf("payment request amount: %v is greater than max allowed: %v", decodedAmt, maxAllowedDeposit),
		}, nil
	}
	log.Printf("GetSwapPayment - paying node %#v amt = %v, maxAllowed = %v", decodedPayReq.Destination, decodedAmt, maxAllowedDeposit)

	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	utxos, err := subswapClient.UnspentAmount(clientCtx, &submarineswaprpc.UnspentAmountRequest{Hash: decodedPayReq.PaymentHash[:]})
	if err != nil {
		return nil, err
	}

	if len(utxos.Utxos) == 0 {
		return nil, status.Errorf(codes.Internal, "there are no UTXOs related to payment request")
	}

	fees, err := subswapClient.SubSwapServiceRedeemFees(clientCtx, &submarineswaprpc.SubSwapServiceRedeemFeesRequest{
		Hash:       decodedPayReq.PaymentHash[:],
		TargetConf: 12,
	})
	if err != nil {
		log.Printf("GetSwapPayment - SubSwapServiceRedeemFees error: %v", err)
		return nil, status.Errorf(codes.Internal, "couldn't determine the redeem transaction fees")
	}
	log.Printf("GetSwapPayment - SubSwapServiceRedeemFees: %v for amount in utxos: %v amount in payment request: %v", fees.Amount, utxos.Amount, decodedAmt)
	if utxos.Amount < 3*fees.Amount {
		return nil, status.Errorf(codes.Internal, "total UTXO not sufficient to create the redeem transaction")
	}

	// Determine if the amount in payment request is the same as in the address UTXOs
	if utxos.Amount != decodedAmt {
		return nil, status.Errorf(codes.Internal, "total UTXO amount not equal to the amount in client's payment request")
	}

	// Get the current blockheight
	chainInfo, err := client.GetInfo(clientCtx, &lnrpc.GetInfoRequest{})
	if err != nil {
		log.Printf("GetSwapPayment - GetInfo error: %v", err)
		return nil, status.Errorf(codes.Internal, "couldn't determine the current blockheight")
	}

	if 4*(int32(chainInfo.BlockHeight)-utxos.Utxos[0].BlockHeight) > 3*utxos.LockHeight {
		return nil, status.Errorf(codes.Internal, "client transaction older than redeem block treshold")
	}

	sendResponse, err := client.SendPaymentSync(clientCtx, &lnrpc.SendRequest{PaymentRequest: in.PaymentRequest})
	if err != nil || sendResponse.PaymentError != "" {
		if sendResponse != nil && sendResponse.PaymentError != "" {
			err = fmt.Errorf("Error in payment response: %v", sendResponse.PaymentError)
		}
		log.Printf("GetSwapPayment - SendPaymentSync paymentRequest: %v, Amount: %v, error: %v", in.PaymentRequest, decodedAmt, err)
		return nil, err
	}

	// Redeem the transaction
	redeem, err := subswapClient.SubSwapServiceRedeem(clientCtx, &submarineswaprpc.SubSwapServiceRedeemRequest{
		Preimage:   sendResponse.PaymentPreimage,
		TargetConf: 12,
	})
	if err != nil {
		log.Printf("GetSwapPayment - couldn't redeem transaction for preimage: %v, error: %v", hex.EncodeToString(sendResponse.PaymentPreimage), err)
		return nil, err
	}

	log.Printf("GetSwapPayment - redeem tx broadcast: %v", redeem.Txid)
	return &breez.GetSwapPaymentReply{PaymentError: sendResponse.PaymentError}, nil
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

//JoinCTPSession is used by both payer/payee to join a CTP session.
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

//Calculate the max allowed deposit for a node
func getMaxAllowedDeposit(nodeID string) (int64, error) {
	log.Println("getMaxAllowedDeposit node ID: ", nodeID)
	maxAllowedToDeposit := int64(depositBalanceThreshold)
	nodeChannels, err := getNodeChannels(nodeID)
	if err != nil {
		return 0, err
	}
	var nodeLocalBalance int64
	for _, ch := range nodeChannels {
		nodeLocalBalance += ch.RemoteBalance
	}
	maxAllowedToDeposit = depositBalanceThreshold - nodeLocalBalance
	if maxAllowedToDeposit < 0 {
		maxAllowedToDeposit = 0
	}
	return maxAllowedToDeposit, nil
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

	lis, err := net.Listen("tcp", os.Getenv("LISTEN_ADDRESS"))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

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
	subswapClient = submarineswaprpc.NewSubmarineSwapperClient(conn)
	go subscribeTransactions()
	go handlePastTransactions()

	err = redisConnect()
	if err != nil {
		log.Println("redisConnect error:", err)
	}
	go deliverSyncNotifications()

	s := grpc.NewServer(
		grpc_middleware.WithUnaryServerChain(
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/RegisterDevice", 3, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/RegisterDevice", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/SendInvoice", 3, 100, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Invoicer/SendInvoice", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.CardOrderer/Order", 3, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.CardOrderer/Order", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/RegisterDevice", 3, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/RegisterDevice", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/UploadLogo", 3, 10, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Pos/UploadLogo", 100, 10000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Ping", 10000, 100000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Ping", 100000, 10000000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Rates", 10000, 100000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.Information/Rates", 100000, 10000000, 86400),
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
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/JoinCTPSession", 10, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/JoinCTPSession", 1000, 100000, 86400),
			ratelimit.PerIPUnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/TerminateCTPSession", 10, 1000, 86400),
			ratelimit.UnaryRateLimiter(redisPool, "rate-limit", "/breez.CTP/TerminateCTPSession", 1009, 100000, 86400),
		),
	)

	breez.RegisterInvoicerServer(s, &server{})
	breez.RegisterPosServer(s, &server{})
	breez.RegisterInformationServer(s, &server{})
	breez.RegisterCardOrdererServer(s, &server{})
	breez.RegisterFundManagerServer(s, &server{})
	breez.RegisterCTPServer(s, &server{})
	breez.RegisterSyncNotifierServer(s, &server{})

	// Register reflection service on gRPC server.
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
