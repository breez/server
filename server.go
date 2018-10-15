package main

// To generate breez/breez.pb.go run:
// protoc -I breez breez/breez.proto --go_out=plugins=grpc:breez

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"github.com/btcsuite/btcutil"
	"image/png"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/breez/server/breez"

	"cloud.google.com/go/storage"
	"github.com/NaySoftware/go-fcm"
	"github.com/breez/lightninglib/lnrpc"
	submarine "github.com/breez/submarinelib"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/gomodule/redigo/redis"
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
	depositBalanceThreshold = 500000
)

var client lnrpc.LightningClient
var network *chaincfg.Params

// server is used to implement breez.InvoicerServer and breez.PosServer
type server struct{}

// FundChannel funds a channel with the specified ID and amount
func (s *server) FundChannel(ctx context.Context, in *breez.FundRequest) (*breez.FundReply, error) {
	if in.Amount <= 0 {
		log.Printf("Funding amount must be more than 0")
		return &breez.FundReply{ReturnCode: breez.FundReply_WRONG_AMOUNT}, nil
	}

	nodePubKey, err := hex.DecodeString(in.LightningID)
	if err != nil {
		log.Printf("Error when calling decoding node ID: %s", err)
		return &breez.FundReply{ReturnCode: breez.FundReply_WRONG_NODE_ID}, err
	}

	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	response, err := client.OpenChannel(clientCtx, &lnrpc.OpenChannelRequest{LocalFundingAmount: in.Amount,
		NodePubkeyString: in.LightningID, NodePubkey: nodePubKey, PushSat: 0, MinHtlcMsat: 600, Private: true})
	if err != nil {
		log.Printf("Error when calling OpenChannel: %s", err)
		return &breez.FundReply{ReturnCode: breez.FundReply_UNKNOWN_ERROR}, err
	}
	log.Printf("Response from server: %s", response)
	return &breez.FundReply{ReturnCode: breez.FundReply_SUCCESS}, nil
}

// RegisterDevice implements breez.InvoicerServer
func (s *server) RegisterDevice(ctx context.Context, in *breez.RegisterRequest) (*breez.RegisterReply, error) {
	return &breez.RegisterReply{BreezID: in.DeviceID}, nil
}

func (s *server) SendInvoice(ctx context.Context, in *breez.PaymentRequest) (*breez.InvoiceReply, error) {
	ids := []string{
		in.BreezID,
	}

	notificationData := map[string]string{
		"msg":          "Payment request",
		"invoice":      in.Invoice,
		"click_action": "FLUTTER_NOTIFICATION_CLICK",
		"collapseKey":  "breez",
	}

	notificationClient := fcm.NewFcmClient(os.Getenv("FCM_KEY"))
	status, err := notificationClient.NewFcmRegIdsMsg(ids, notificationData).
		SetPriority(fcm.Priority_HIGH).
		SetNotificationPayload(&fcm.NotificationPayload{Title: in.Payee,
			Body:  "is requesting you to pay " + strconv.FormatInt(in.Amount, 10) + " Sat",
			Icon:  "ic_launcher",
			Sound: "default"}).
		Send()

	status.PrintResults()
	if err != nil {
		log.Println(status)
		log.Println(err)
		return &breez.InvoiceReply{Error: err.Error()}, err
	}

	data := map[string]string{
		"payment_request": in.Invoice,
		"payee":           in.Payee,
		"amount":          strconv.FormatInt(in.Amount, 10),
		"collapseKey":     "breez",
	}

	dataClient := fcm.NewFcmClient(os.Getenv("FCM_KEY"))
	dataStatus, err := dataClient.NewFcmRegIdsMsg(ids, data).
		SetPriority(fcm.Priority_HIGH).
		SetMsgData(data).
		Send()

	dataStatus.PrintResults()
	if err != nil {
		log.Println(status)
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

func (s *server) OpenChannel(ctx context.Context, in *breez.OpenChannelRequest) (*breez.OpenChannelReply, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	nodeChannels, err := getNodeChannels(in.PubKey)
	if err != nil {
		return nil, err
	}
	if len(nodeChannels) == 0 {
		response, err := client.OpenChannelSync(clientCtx, &lnrpc.OpenChannelRequest{LocalFundingAmount: channelAmount,
			NodePubkeyString: in.PubKey, PushSat: 0, MinHtlcMsat: 600, Private: true})
		log.Printf("Response from OpenChannel: %#v (TX: %v)", response, hex.EncodeToString(response.GetFundingTxidBytes()))
		if err != nil {
			return nil, err
		}
	}
	return &breez.OpenChannelReply{}, nil
}

func (s *server) AddFund(ctx context.Context, in *breez.AddFundRequest) (*breez.AddFundReply, error) {

	maxAllowedDeposit, err := getMaxAllowedDeposit(in.LightningNodeId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to calculate max allowed deposit amount")
	}

	if maxAllowedDeposit == 0 {
		return &breez.AddFundReply{MaxAllowedDeposit: maxAllowedDeposit, ErrorMessage: fmt.Sprintf("Adding funds is enabled when the balance is under %v", depositBalanceThreshold)}, nil
	}

	chainPublicKey, chainPrivateKey, err := submarine.GenPublicPrivateKeypair()
	if err != nil {
		return nil, err
	}

	// Create a script with our and client's data
	script, err := submarine.GenSubmarineSwapScript(chainPublicKey, in.ChainPublicKey, in.PaymentHash, int64(72))
	if err != nil {
		return nil, err
	}

	// Now it's time to create a nice address
	address := submarine.GenBase58Address(script, network)

	// Make ligtninglib monitor the address
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	_, err = client.WatchAddress(clientCtx, &lnrpc.WatchAddressRequest{Address: address})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "couldn't add address to watchlist")
	}

	// Save everything
	redisConn := redisPool.Get()
	defer redisConn.Close()
	_, err = redisConn.Do("HMSET", "input-address:"+address, "clientChainPublicKey", in.ChainPublicKey, "clientLightningPublicKey", in.PaymentHash, "ourChainPrivateKey", chainPrivateKey, "serializedScript", script)
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
	return &breez.AddFundReply{Address: address, ChainPublicKey: chainPublicKey, MaxAllowedDeposit: maxAllowedDeposit}, nil
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

func (s *server) GetPayment(ctx context.Context, in *breez.GetPaymentRequest) (*breez.GetPaymentReply, error) {
	redisConn := redisPool.Get()
	m, err := redis.StringMap(redisConn.Do("HGETALL", "input-address:"+in.Address))
	if err != nil {
		log.Println("GetPayment error:", err)
		return nil, status.Errorf(codes.Internal, "failed to retrieve address information")
	}

	//ensure we didn't pay this invoice before
	preImage := m["payment:PaymentPreimage"]
	if preImage != "" {
		return nil, status.Errorf(codes.Internal, "payment already sent")
	}

	amt, err := strconv.ParseInt(m["tx:Amount"], 10, 64)
	if err != nil || amt <= 0 {
		return nil, status.Errorf(codes.Internal, "on-chain funds are not confirmred yet")
	}

	fee, err := strconv.ParseInt(m["utx:TotalFee"], 10, 64)
	if err != nil {
		fee = 0
	}

	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	payReq, err := client.DecodePayReq(clientCtx, &lnrpc.PayReqString{PayReq: in.PaymentRequest})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "payment request is not valid")
	}
	maxAllowedDeposit, err := getMaxAllowedDeposit(payReq.Destination)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to calculate max allowed deposit amount")
	}
	if amt > maxAllowedDeposit {
		return nil, status.Errorf(codes.Internal, fmt.Sprintf("deposit amount: %v is greater than max allowed: %v", amt, maxAllowedDeposit))
	}
	log.Printf("paying node %v amt = %v, maxAllowed = %v", payReq.Destination, amt, maxAllowedDeposit)

	sendResponse, err := client.SendPaymentSync(clientCtx, &lnrpc.SendRequest{PaymentRequest: in.PaymentRequest, Amt: amt})
	if err != nil {
		log.Printf("SendPaymentSync address: %v, paymentRequest: %v, Amount: %v, error: %v", in.Address, in.PaymentRequest, amt, err)
		return nil, status.Errorf(codes.Internal, "failed to send payment")
	}

	if sendResponse.PaymentError != "" {
		log.Printf("SendPaymentSync payment address: %v, paymentRequest: %v, Amount: %v, error: %v", in.Address, in.PaymentRequest, amt, sendResponse.PaymentError)
	} else {
		// Save the preimage to make sure we don't pay the same invoice again
		_, err = redisConn.Do("HSET", "input-address:"+in.Address,
			"payment:PaymentPreimage", sendResponse.PaymentPreimage,
		)
		if err != nil {
			log.Printf("handleTransactionAddress error in HSET preimage for %v: Preimage: %v: %v", in.Address, hex.EncodeToString(sendResponse.PaymentPreimage), err) //here we have nothing to do. We didn't store the fact that we paid the user
		}

		_, err = redisConn.Do("SREM", "fund-addresses", in.Address)
		if err != nil {
			log.Printf("handleTransactionAddress error in SREM %v from fund-addresses: %v", in.Address, err)
		}

		// Get a new wallet address to redeem the swap funds to
		addressResponse, err := client.NewAddress(clientCtx, &lnrpc.NewAddressRequest{Type: lnrpc.NewAddressRequest_WITNESS_PUBKEY_HASH})
		if err != nil {
			log.Printf("couldn't issue a new address from the lightning wallet: %v", err)
		}

		redeemAddress, err := btcutil.DecodeAddress(addressResponse.Address, nil)
		if err != nil {
			return nil, err
		}

		txHash, _ := hex.DecodeString(m["tx:TxHash"])

		var txHashArray [32]byte
		copy(txHashArray[:], txHash[0:32])

		// Make a redeem transaction
		redeemTx, err := submarine.GetRedeemTransaction(amt, fee, txHashArray, []byte(m["serializedScript"]), []byte(m["ourChainPrivateKey"]), sendResponse.PaymentPreimage, redeemAddress)
		if err != nil {
			log.Printf("couldn't make the redeem transaction: %v", err)
		}

		// Broadcast the redeem transaction to network at large
		_, err = client.SendRawTx(clientCtx, &lnrpc.SendRawTxRequest{Hextx: redeemTx})
		if err != nil {
			log.Printf("couldn't broadcast redeem transaction: %v", err)
		}
	}

	return &breez.GetPaymentReply{PaymentError: sendResponse.PaymentError}, nil
}

func (s *server) RemoveFund(ctx context.Context, in *breez.RemoveFundRequest) (*breez.RemoveFundReply, error) {
	//TODO
	return nil, nil
}

// RegisterDevice implements breez.InvoicerServer
func (s *server) Order(ctx context.Context, in *breez.OrderRequest) (*breez.OrderReply, error) {
	log.Println("Order a card for:", *in)
	return &breez.OrderReply{}, nil
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
	go subscribeTransactions()
	go handlePastTransactions()

	err = redisConnect()
	if err != nil {
		log.Println("redisConnect error:", err)
	}

	go notify()

	s := grpc.NewServer()

	breez.RegisterInvoicerServer(s, &server{})
	breez.RegisterPosServer(s, &server{})
	breez.RegisterInformationServer(s, &server{})
	breez.RegisterCardOrdererServer(s, &server{})
	breez.RegisterFundManagerServer(s, &server{})

	// Register reflection service on gRPC server.
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
