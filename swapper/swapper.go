package swapper

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/breez/server/bitcoind"
	"github.com/breez/server/breez"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/gomodule/redigo/redis"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/submarineswaprpc"
	"github.com/lightningnetwork/lnd/lnrpc/walletrpc"
	"github.com/lightningnetwork/lnd/zpay32"
	"golang.org/x/text/message"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	depositBalanceThresholdLegacy = 900_000
	depositBalanceThreshold       = 4_000_000
	minRemoveFund                 = 50000
	chanReserve                   = 600
)

// Server implements lsp grpc functions
type Server struct {
	breez.UnimplementedSwapperServer
	network               *chaincfg.Params
	redisPool             *redis.Pool
	client                lnrpc.LightningClient
	ssClient              lnrpc.LightningClient
	subswapClient         submarineswaprpc.SubmarineSwapperClient
	redeemer              *Redeemer
	walletKitClient       walletrpc.WalletKitClient
	ssRouterClient        routerrpc.RouterClient
	insertSubswapPayment  func(paymentHash, paymentRequest string, lockheight, confirmationheight int32, utxos []string) error
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error
	hasFilteredAddress    func(addrs []string) (bool, error)
	ReverseRoutingNodeID  []byte
}

func NewServer(
	network *chaincfg.Params,
	redisPool *redis.Pool,
	client, ssClient lnrpc.LightningClient,
	subswapClient submarineswaprpc.SubmarineSwapperClient,
	redeemer *Redeemer,
	walletKitClient walletrpc.WalletKitClient,
	ssRouterClient routerrpc.RouterClient,
	insertSubswapPayment func(paymentHash, paymentRequest string, lockheight, confirmationheight int32, utxos []string) error,
	updateSubswapPreimage func(paymentHash, paymentPreimage string) error,
	hasFilteredAddress func(addrs []string) (bool, error),
) *Server {
	nodeID, err := hex.DecodeString(os.Getenv("REVERSE_SWAP_ROUTING_NODE"))
	if err != nil {
		log.Panicf("GetReverseRoutingNode error in hex.DecodeString(%v): %v", os.Getenv("REVERSE_SWAP_ROUTING_NODE"), err)
	}
	return &Server{
		network:               network,
		redisPool:             redisPool,
		client:                client,
		ssClient:              ssClient,
		subswapClient:         subswapClient,
		redeemer:              redeemer,
		walletKitClient:       walletKitClient,
		ssRouterClient:        ssRouterClient,
		insertSubswapPayment:  insertSubswapPayment,
		updateSubswapPreimage: updateSubswapPreimage,
		hasFilteredAddress:    hasFilteredAddress,
		ReverseRoutingNodeID:  nodeID,
	}
}

func (s *Server) AddFundInitLegacy(ctx context.Context, in *breez.AddFundInitRequest) (*breez.AddFundInitReply, error) {
	return s.addFundInit(ctx, in, depositBalanceThresholdLegacy)
}
func (s *Server) AddFundInit(ctx context.Context, in *breez.AddFundInitRequest) (*breez.AddFundInitReply, error) {
	return s.addFundInit(ctx, in, depositBalanceThreshold)
}

func (s *Server) addFundInit(ctx context.Context, in *breez.AddFundInitRequest, max int64) (*breez.AddFundInitReply, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("SUBSWAPPER_LND_MACAROON_HEX"))

	maxAllowedDeposit, err := s.getMaxAllowedDeposit(in.NodeID, max)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to calculate max allowed deposit amount")
	}

	if maxAllowedDeposit == 0 {
		p := message.NewPrinter(message.MatchLanguage("en"))
		satFormatted := strings.Replace(p.Sprintf("%d", max), ",", " ", 1)
		btcFormatted := strconv.FormatFloat(float64(max)/float64(100000000), 'f', -1, 64)
		return &breez.AddFundInitReply{
			MaxAllowedDeposit: maxAllowedDeposit,
			ErrorMessage: fmt.Sprintf("Adding funds is enabled when the balance is under %v BTC (%v Sat).",
				btcFormatted, satFormatted),
			RequiredReserve: chanReserve,
		}, nil
	}

	subSwapServiceInitResponse, err := s.subswapClient.SubSwapServiceInit(clientCtx, &submarineswaprpc.SubSwapServiceInitRequest{
		Hash:   in.Hash,
		Pubkey: in.Pubkey,
	})
	if err != nil {
		log.Printf("subswapClient.SubSwapServiceInit (hash:%v, pubkey:%v) error: %v", in.Hash, in.Pubkey, err)
		return nil, err
	}

	var minAllowedDeposit int64
	ct := 12
	fees, err := s.walletKitClient.EstimateFee(clientCtx, &walletrpc.EstimateFeeRequest{ConfTarget: int32(ct)})
	if err != nil {
		log.Printf("walletKitClient.EstimateFee(%v) error: %v", ct, err)
	} else {
		log.Printf("walletKitClient.EstimateFee(%v): %v", ct, fees.SatPerKw)
		// Assume a weight of 1K for the transaction.
		minAllowedDeposit = fees.SatPerKw * 3 / 2
	}

	address := subSwapServiceInitResponse.Address
	redisConn := s.redisPool.Get()
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
		MinAllowedDeposit: minAllowedDeposit,
	}, nil
}

func (s *Server) AddFundStatus(ctx context.Context, in *breez.AddFundStatusRequest) (*breez.AddFundStatusReply, error) {
	statuses := make(map[string]*breez.AddFundStatusReply_AddressStatus)
	redisConn := s.redisPool.Get()
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
func (s *Server) GetSwapPayment(ctx context.Context, in *breez.GetSwapPaymentRequest) (*breez.GetSwapPaymentReply, error) {
	return s.getSwapPayment(ctx, in, depositBalanceThreshold)
}

func (s *Server) GetSwapPaymentLegacy(ctx context.Context, in *breez.GetSwapPaymentRequest) (*breez.GetSwapPaymentReply, error) {
	return s.getSwapPayment(ctx, in, depositBalanceThresholdLegacy)
}

func (s *Server) getSwapPayment(ctx context.Context, in *breez.GetSwapPaymentRequest, max int64) (*breez.GetSwapPaymentReply, error) {
	// Decode the the client's payment request
	decodedPayReq, err := zpay32.Decode(in.PaymentRequest, s.network)
	if err != nil {
		log.Printf("GetSwapPayment - Error in zpay32.Decode: %v", err)
		return nil, status.Errorf(codes.Internal, "payment request is not valid")
	}

	decodedAmt := int64(0)
	if decodedPayReq.MilliSat != nil {
		decodedAmt = int64(decodedPayReq.MilliSat.ToSatoshis())
	}

	maxAllowedDeposit, err := s.getMaxAllowedDeposit(hex.EncodeToString(decodedPayReq.Destination.SerializeCompressed()), max)
	if err != nil {
		log.Printf("GetSwapPayment - getMaxAllowedDeposit error: %v", err)
		return nil, status.Errorf(codes.Internal, "failed to calculate max allowed deposit amount")
	}
	if decodedAmt > maxAllowedDeposit {
		log.Printf("GetSwapPayment - decodedAmt > maxAllowedDeposit: %v > %v", decodedAmt, maxAllowedDeposit)
		return &breez.GetSwapPaymentReply{
			FundsExceededLimit: true,
			SwapError:          breez.GetSwapPaymentReply_FUNDS_EXCEED_LIMIT,
			PaymentError:       fmt.Sprintf("payment request amount: %v is greater than max allowed: %v", decodedAmt, maxAllowedDeposit),
		}, nil
	}
	log.Printf("GetSwapPayment - paying node %x amt = %v, maxAllowed = %v", decodedPayReq.Destination.SerializeCompressed(), decodedAmt, maxAllowedDeposit)

	subswapClientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("SUBSWAPPER_LND_MACAROON_HEX"))
	utxos, err := s.subswapClient.UnspentAmount(subswapClientCtx, &submarineswaprpc.UnspentAmountRequest{Hash: decodedPayReq.PaymentHash[:]})
	if err != nil {
		return nil, err
	}

	if len(utxos.Utxos) == 0 {
		return nil, status.Errorf(codes.Internal, "there are no UTXOs related to payment request")
	}

	fees, err := s.subswapClient.SubSwapServiceRedeemFees(subswapClientCtx, &submarineswaprpc.SubSwapServiceRedeemFeesRequest{
		Hash:       decodedPayReq.PaymentHash[:],
		TargetConf: 6,
	})
	if err != nil {
		log.Printf("GetSwapPayment - SubSwapServiceRedeemFees error: %v", err)
		return nil, status.Errorf(codes.Internal, "couldn't determine the redeem transaction fees")
	}
	log.Printf("GetSwapPayment - SubSwapServiceRedeemFees: %v for amount in utxos: %v amount in payment request: %v", fees.Amount, utxos.Amount, decodedAmt)
	if 2*utxos.Amount < 3*fees.Amount {
		log.Println("GetSwapPayment - utxo amount less than 1.5 fees. Cannot proceed")
		return &breez.GetSwapPaymentReply{
			FundsExceededLimit: true,
			SwapError:          breez.GetSwapPaymentReply_TX_TOO_SMALL,
			PaymentError:       "total UTXO not sufficient to create the redeem transaction",
		}, nil
	}

	// Determine if the amount in payment request is the same as in the address UTXOs
	if utxos.Amount != decodedAmt {
		log.Printf("GetSwapPayment error - utxos.Amount: %v != decodedAmt: %v", utxos.Amount, decodedAmt)
		return &breez.GetSwapPaymentReply{
			FundsExceededLimit: true,
			SwapError:          breez.GetSwapPaymentReply_INVOICE_AMOUNT_MISMATCH,
			PaymentError:       "total UTXO amount not equal to the amount in client's payment request",
		}, nil
	}

	// Get the current blockheight
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	chainInfo, err := s.client.GetInfo(clientCtx, &lnrpc.GetInfoRequest{})
	if err != nil {
		log.Printf("GetSwapPayment - GetInfo error: %v", err)
		return nil, status.Errorf(codes.Internal, "couldn't determine the current blockheight")
	}

	// Get the oldest height of the utxos and the utxo ids
	utxoids := make([]string, len(utxos.Utxos))
	utxoids[0] = fmt.Sprintf("%s:%d", utxos.Utxos[0].Txid, utxos.Utxos[0].Index)
	minHeight := utxos.Utxos[0].BlockHeight
	for i := 1; i < len(utxos.Utxos); i++ {
		utxoids[i] = fmt.Sprintf("%s:%d", utxos.Utxos[i].Txid, utxos.Utxos[i].Index)
		if utxos.Utxos[i].BlockHeight < minHeight {
			minHeight = utxos.Utxos[i].BlockHeight
		}
	}

	if 4*(int32(chainInfo.BlockHeight)-minHeight) > 3*utxos.LockHeight {
		log.Printf("Client transaction height: %v older than redeem block treshold", minHeight)
		return &breez.GetSwapPaymentReply{
			FundsExceededLimit: true,
			SwapError:          breez.GetSwapPaymentReply_SWAP_EXPIRED,
			PaymentError:       "client transaction older than redeem block treshold",
		}, nil
	}
	txids := []string{}
	for _, u := range utxos.Utxos {
		txids = append(txids, u.Txid)
	}
	addrs, _ := bitcoind.GetSenderAddresses(txids)
	hasFiltered, _ := s.hasFilteredAddress(addrs)
	if hasFiltered {
		log.Printf("GetSwapPayment - hasSanc")
		return nil, status.Errorf(codes.Internal, "fa internal error")
	}

	err = s.insertSubswapPayment(
		hex.EncodeToString(decodedPayReq.PaymentHash[:]),
		in.PaymentRequest,
		utxos.LockHeight,
		minHeight,
		utxoids,
	)
	if err != nil {
		log.Printf("GetSwapPayment - insertSubswapPayment paymentRequest: %v, error: %v", in.PaymentRequest, err)
		return nil, fmt.Errorf("error in insertSubswapPayment: %w", err)
	}
	_, err = s.ssRouterClient.ResetMissionControl(subswapClientCtx, &routerrpc.ResetMissionControlRequest{})
	if err != nil {
		log.Printf("GetSwapPayment - ResetMissionControl paymentRequest: %v, error: %v", in.PaymentRequest, err)
	}
	sendResponse, err := s.ssClient.SendPaymentSync(subswapClientCtx, &lnrpc.SendRequest{PaymentRequest: in.PaymentRequest})
	if err != nil {
		log.Printf("GetSwapPayment - SendPaymentSync paymentRequest: %v, Amount: %v, error: %v", in.PaymentRequest, decodedAmt, err)
		return nil, err
	}
	if sendResponse.PaymentError != "" {
		if strings.Contains(sendResponse.PaymentError, "no_route") {
			err = fmt.Errorf("error in payment response: %v", sendResponse.PaymentError+" - TemporaryChannelFailure")
		} else {
			err = fmt.Errorf("error in payment response: %v", sendResponse.PaymentError)
		}

		log.Printf("GetSwapPayment - SendPaymentSync paymentRequest: %v, Amount: %v, Preimage: %x error: %v", in.PaymentRequest, decodedAmt, sendResponse.PaymentPreimage, err)
		return nil, err
	}

	err = s.updateSubswapPreimage(hex.EncodeToString(sendResponse.PaymentHash), hex.EncodeToString(sendResponse.PaymentPreimage))
	if err != nil {
		log.Printf("Failed to update subswap preimage '%x' for payment hash '%x', error: %v", sendResponse.PaymentPreimage, sendResponse.PaymentHash, err)
	}

	_, err = s.redeemer.RedeemWithinBlocks(sendResponse.PaymentPreimage, utxos.LockHeight-(int32(chainInfo.BlockHeight)-minHeight))
	if err != nil {
		log.Printf("RedeemWithinBlocks - couldn't redeem transaction for preimage: %x, error: %v", sendResponse.PaymentPreimage, err)
	}

	return &breez.GetSwapPaymentReply{PaymentError: sendResponse.PaymentError}, nil
}

func (s *Server) RedeemSwapPayment(ctx context.Context, in *breez.RedeemSwapPaymentRequest) (*breez.RedeemSwapPaymentReply, error) {
	txid, err := s.redeemer.RedeemWithFees(in.Preimage, in.TargetConf, in.SatPerByte)
	if err != nil {
		return nil, err
	}

	return &breez.RedeemSwapPaymentReply{Txid: txid}, nil
}

// Calculate the max allowed deposit for a node
func (s *Server) getMaxAllowedDeposit(nodeID string, max int64) (int64, error) {
	log.Println("getMaxAllowedDeposit node ID: ", nodeID)
	nodeChannels, err := s.getNodeChannels(nodeID)
	if err != nil {
		return 0, err
	}
	var nodeLocalBalance int64
	for _, ch := range nodeChannels {
		nodeLocalBalance += ch.RemoteBalance
	}
	maxAllowedToDeposit := max - nodeLocalBalance
	if maxAllowedToDeposit < 0 {
		maxAllowedToDeposit = 0
	}
	return maxAllowedToDeposit, nil
}

func (s *Server) getNodeChannels(nodeID string) ([]*lnrpc.Channel, error) {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	listResponse, err := s.client.ListChannels(clientCtx, &lnrpc.ListChannelsRequest{})
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

func (s *Server) GetReverseRoutingNode(ctx context.Context, in *breez.GetReverseRoutingNodeRequest) (*breez.GetReverseRoutingNodeReply, error) {
	return &breez.GetReverseRoutingNodeReply{NodeId: s.ReverseRoutingNodeID}, nil
}

func (s *Server) RedeemSwapPayments() {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	listPaymentsResponse, err := s.client.ListPayments(clientCtx, &lnrpc.ListPaymentsRequest{
		IncludeIncomplete: true,
		MaxPayments:       100000,
	})
	_ = err
	_ = listPaymentsResponse.Payments[0]
}
