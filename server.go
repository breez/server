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
	imageDimensionLength = 200
	channelAmount        = 1000000
	userAmountMax        = 500000
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

func (s *server) MempoolRegister(ctx context.Context, in *breez.MempoolRegisterRequest) (*breez.MempoolRegisterReply, error) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	for _, a := range in.Addresses {
		redisConn.Do("SET", "address:"+a, in.ClientID, "EX", 24*3600)
	}
	var txs []*breez.MempoolRegisterReply_Transaction
	for _, a := range in.Addresses {
		_ = a
		transactions, err := redis.Strings(redisConn.Do("SMEMBERS", "transactions:"+a))
		if err == nil {
			for _, transaction := range transactions {
				m, err := redis.StringMap(redisConn.Do("HGETALL", transaction))
				if err == nil {
					if m["clientID"] == in.ClientID {
						if value, err := strconv.ParseFloat(m["value"], 64); err == nil {
							tx := &breez.MempoolRegisterReply_Transaction{
								TX:      m["tx"],
								Address: m["address"],
								Value:   value,
							}
							txs = append(txs, tx)
						}
					}
				} else if err != redis.ErrNil {
					log.Println("Error in HMGET")
				}
			}
		} else if err != redis.ErrNil {
			log.Println("Error in SMEMEMBERS:", err)
		}
	}
	return &breez.MempoolRegisterReply{TXS: txs}, nil
}

func (s *server) OpenChannel(ctx context.Context, in *breez.OpenChannelRequest) (*breez.OpenChannelReply, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	listResponse, err := client.ListChannels(clientCtx, &lnrpc.ListChannelsRequest{})
	if err != nil {
		return nil, err
	}
	channels := 0
	for _, channel := range listResponse.Channels {
		if channel.RemotePubkey == in.PubKey {
			channels++
		}
	}
	if channels == 0 {
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
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	newAddrResp, err := client.NewWitnessAddress(clientCtx, &lnrpc.NewWitnessAddressRequest{})
	if err != nil {
		return nil, err
	}
	address := newAddrResp.Address

	redisConn := redisPool.Get()
	defer redisConn.Close()
	_, err = redisConn.Do("HMSET", "input-address:"+address, "paymentRequest", in.PaymentRequest)
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
	return &breez.AddFundReply{Address: address}, nil
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

func (s *server) RemoveFund(ctx context.Context, in *breez.RemoveFundRequest) (*breez.RemoveFundReply, error) {
	//TODO
	return nil, nil
}

// RegisterDevice implements breez.InvoicerServer
func (s *server) Order(ctx context.Context, in *breez.OrderRequest) (*breez.OrderReply, error) {
	log.Println("Order a card for:", *in)
	return &breez.OrderReply{}, nil
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

	err = btcdConnect()
	if err != nil {
		log.Println("btcdConnect error:", err)
	}

	go notify()

	s := grpc.NewServer()

	breez.RegisterInvoicerServer(s, &server{})
	breez.RegisterPosServer(s, &server{})
	breez.RegisterInformationServer(s, &server{})
	breez.RegisterCardOrdererServer(s, &server{})
	breez.RegisterMempoolNotifierServer(s, &server{})
	breez.RegisterFundManagerServer(s, &server{})

	// Register reflection service on gRPC server.
	reflection.Register(s)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
