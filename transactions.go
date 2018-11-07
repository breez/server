package main

import (
	"context"
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

var txGroup singleflight.Group

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
				go notifyClientTransaction(tx, i, "Confirmed transaction", "Breez", "Funds added to Breez are now confirmed. Please open the app to complete your setup.")
				break // There is only one address concerning us per transaction
			} else {
				redisConn := redisPool.Get()
				defer redisConn.Close()
				_, err := redisConn.Do("HMSET", "input-address:"+tx.DestAddresses[i],
					"utx:TxHash", tx.TxHash,
					"utx:Amount", tx.Amount,
				)
				if err != nil {
					log.Println("handleTransaction error:", err)
					return err
				}
				amt := strconv.FormatInt(tx.Amount, 10)
				go notifyClientTransaction(tx, i, "Unconfirmed transaction", "Breez", "Breez is waiting for "+amt+" sat to be confirmed. Confirmation usually takes ~10 minutes to be completed.")
				break
			}
		}
	}
	return nil
}

func notifyClientTransaction(tx *lnrpc.Transaction, index int, msg, title, body string) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	tokens, err := redis.Strings(redisConn.Do("SMEMBERS", "input-address-notification:"+tx.DestAddresses[index]))
	if err != nil {
		log.Println("notifyUnconfirmed error:", err)
		return
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
			if result["error"] == "Unregistered Device" {
				_, err = redisConn.Do("SREM", "input-address-notification:"+tx.DestAddresses[index], tokens[i])
				if err != nil {
					log.Println("Error in send:", err)
				}
			}
		}
	}

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
