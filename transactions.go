package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/NaySoftware/go-fcm"
	"github.com/breez/lightninglib/lnrpc"
	"github.com/gomodule/redigo/redis"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/metadata"
)

const (
	receivePaymentType            = "receivePayment"
	channelOpenedType             = "channelOpened"
	transactionNotificationExpiry = 3600 * 6
)

var (
	txGroup             singleflight.Group
	txNotificationGroup singleflight.Group
	notificationTypes   = map[string]map[string]string{
		receivePaymentType: map[string]string{
			"title": "Receive Payment",
			"body":  "You are now ready to receive payments using Breez. Open to continue with a previously shared payment link.",
		},
		channelOpenedType: map[string]string{
			"title": "Breez",
			"body":  "You can now use Breez to send and receive Bitcoin payments!",
		},
	}
)

func handlePastTransactions() error {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	transactionDetails, err := client.GetTransactions(clientCtx, &lnrpc.GetTransactionsRequest{})
	if err != nil {
		log.Println("handlePastTransactions error:", err)
		return err
	}
	for _, t := range transactionDetails.Transactions {
		key := t.TxHash
		if t.NumConfirmations == 0 {
			key = key + "-unconfirmed"
		} else {
			key = key + "-confirmed"
		}
		_, err, _ = txGroup.Do(key, func() (interface{}, error) {
			err := handleTransaction(t)
			return nil, err
		})
		if err != nil {
			log.Println("handleTransaction error:", err)
		}
	}
	return nil
}

func subscribeTransactions() {
	for {
		log.Println("new subscribe")
		err := subscribeTransactionsOnce()
		if err != nil {
			log.Println("subscribeTransactions:", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func subscribeTransactionsOnce() error {
	clientCtx := metadata.AppendToOutgoingContext(context.Background(), "macaroon", os.Getenv("LND_MACAROON_HEX"))
	transactionStream, err := client.SubscribeTransactions(clientCtx, &lnrpc.GetTransactionsRequest{})
	if err != nil {
		log.Println("SubscribeTransactions:", err)
		return err
	}
	for {
		log.Println("new Recv call")
		t, err := transactionStream.Recv()
		if err == io.EOF {
			log.Println("Stream stopped. Need to re-registser")
			break
		}
		if err != nil {
			log.Printf("Error in stream: %v", err)
			return err
		}
		//log.Printf("t:%#v", t)
		key := t.TxHash
		if t.NumConfirmations == 0 {
			key = key + "-unconfirmed"
		} else {
			key = key + "-confirmed"
		}
		_, err, _ = txGroup.Do(key, func() (interface{}, error) {
			err := handleTransaction(t)
			return nil, err
		})
		if err != nil {
			log.Println("handleTransaction error:", err)
		}
	}
	return nil
}

func handleTransaction(tx *lnrpc.Transaction) error {
	if err := handleTransactionNotifications(tx); err != nil {
		log.Println("handleTransactionNotifications error:", err)
		return err
	}
	return handleTransactionAddreses(tx)
}

func registerTransacionConfirmation(txID, token, notifyType string) error {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	registrationKey := fmt.Sprintf("tx-notify-%v", txID)
	registrationData := map[string]string{"token": token, "type": notifyType}
	marshalled, err := json.Marshal(registrationData)
	if err != nil {
		return err
	}
	_, err = redisConn.Do("SADD", registrationKey, string(marshalled))
	if err != nil {
		return err
	}
	err = setKeyExpiration(registrationKey, transactionNotificationExpiry)
	return err
}

func handleTransactionNotifications(tx *lnrpc.Transaction) error {
	if tx.NumConfirmations == 0 {
		return nil
	}

	registrationKey := fmt.Sprintf("tx-notify-%v", tx.TxHash)
	redisConn := redisPool.Get()
	defer redisConn.Close()
	for {
		registrations, err := redis.Strings(redisConn.Do("SPOP", registrationKey, 10))
		if err != nil {
			return err
		}

		for _, r := range registrations {
			var regData map[string]string
			if err = json.Unmarshal([]byte(r), &regData); err != nil {
				log.Printf("Failed to decode json registration %v", r)
				continue
			}
			notificationType := regData["type"]
			notificationToken := regData["token"]
			notifyConfig := defaultNotificationConfig()
			notifyConfig.title = notificationTypes[notificationType]["title"]
			notifyConfig.body = notificationTypes[notificationType]["body"]
			go notify(notifyConfig, []string{notificationToken})
		}

		if len(registrations) < 10 {
			break
		}
	}
	return nil
}

func handleTransactionAddreses(tx *lnrpc.Transaction) error {
	log.Printf("t:%#v", tx)
	redisConn := redisPool.Get()
	defer redisConn.Close()
	for i, a := range tx.DestAddresses {
		n, err := redis.Int(redisConn.Do("SISMEMBER", "fund-addresses", a))
		if err != nil {
			log.Println("handleTransaction error:", err)
			return err
		}
		if n > 0 {
			if tx.NumConfirmations > 0 {
				err = handleTransactionAddress(tx, i)
				if err != nil {
					return err
				}
				go notifyClientTransaction(tx, i, "Confirmed transaction", "Breez", "Funds added to Breez are now confirmed. Please open the app to complete your setup.", true)
				break // There is only one address concerning us per transaction
			} else {
				redisConn := redisPool.Get()
				defer redisConn.Close()
				_, err := redisConn.Do("HMSET", "input-address:"+tx.DestAddresses[i],
					"utx:TxHash", tx.TxHash,
					"utx:Amount", tx.Amount,
				)
				if err != nil {
					log.Println("handleTransactionAddreses error:", err)
					return err
				}
				amt := strconv.FormatInt(tx.Amount, 10)
				go notifyClientTransaction(tx, i, "Unconfirmed transaction", "Breez", "Breez is waiting for "+amt+" sat to be confirmed. Confirmation usually takes ~10 minutes to be completed.", false)
				break
			}
		}
	}
	return nil
}

func notifyClientTransaction(tx *lnrpc.Transaction, index int, msg, title, body string, delete bool) {
	key := tx.TxHash + "-notification"
	_, _, _ = txNotificationGroup.Do(key, func() (interface{}, error) {
		redisConn := redisPool.Get()
		defer redisConn.Close()
		tokens, err := redis.Strings(redisConn.Do("SMEMBERS", "input-address-notification:"+tx.DestAddresses[index]))
		if err != nil {
			log.Println("notifyUnconfirmed error:", err)
			return nil, nil
		}
		data := map[string]string{
			"msg":          msg,
			"tx":           tx.TxHash,
			"address":      tx.DestAddresses[index],
			"value":        strconv.FormatInt(tx.Amount, 10),
			"click_action": "FLUTTER_NOTIFICATION_CLICK",
		}
		fcmClient := fcm.NewFcmClient(os.Getenv("FCM_KEY"))
		status, err := fcmClient.NewFcmRegIdsMsg(tokens, data).
			SetNotificationPayload(&fcm.NotificationPayload{Title: title,
				Body:  body,
				Icon:  "breez_notify",
				Sound: "default"}).
			SetPriority(fcm.Priority_HIGH).
			Send()

		status.PrintResults()
		if err != nil {
			log.Println("Error in send:", err)
		} else {
			for i, result := range status.Results {
				if result["error"] == "Unregistered Device" || delete {
					_, err = redisConn.Do("SREM", "input-address-notification:"+tx.DestAddresses[index], tokens[i])
					if err != nil {
						log.Printf("Error in notifyClientTransaction (SREM); set:%v member:%v error:%v", "input-address-notification:"+tx.DestAddresses[index], tokens[i], err)
					}
				}
			}
			card, err := redis.Int(redisConn.Do("SCARD", "input-address-notification:"+tx.DestAddresses[index]))
			if err != nil {
				log.Printf("Error in notifyClientTransaction (SCARD); set:%v error:%v", "input-address-notification:"+tx.DestAddresses[index], err)
			} else {
				if card == 0 {
					_, err = redisConn.Do("DEL", "input-address-notification:"+tx.DestAddresses[index])
					if err != nil {
						log.Printf("Error in notifyClientTransaction (DEL); set:%v error:%v", "input-address-notification:"+tx.DestAddresses[index], err)
					}
				}
			}
		}
		return nil, nil
	})
}

func handleTransactionAddress(tx *lnrpc.Transaction, index int) error {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	_, err := redisConn.Do("HMSET", "input-address:"+tx.DestAddresses[index],
		"tx:TxHash", tx.TxHash,
		"tx:Amount", tx.Amount,
		"tx:BlockHash", tx.BlockHash,
	)
	if err != nil {
		log.Println("handleTransactionAddress error:", err)
		return err
	}
	return nil
}
