package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/gomodule/redigo/redis"
	"github.com/lightningnetwork/lnd/lnrpc"
	"golang.org/x/sync/singleflight"
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

func handlePastTransactions(ctx context.Context, c lnrpc.LightningClient) error {
	transactionDetails, err := c.GetTransactions(ctx, &lnrpc.GetTransactionsRequest{})
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

func subscribeTransactions(ctx context.Context, c lnrpc.LightningClient) {
	for {
		log.Println("new subscribe")
		err := subscribeTransactionsOnce(ctx, c)
		if err != nil {
			log.Println("subscribeTransactions:", err)
		}
		time.Sleep(5 * time.Second)
	}
}

func destAddresses(tx *lnrpc.Transaction) ([]string, error) {
	if len(tx.DestAddresses) > 0 {
		return tx.DestAddresses, nil
	}
	if tx.RawTxHex == "" {
		return nil, nil
	}
	rawTx, err := hex.DecodeString(tx.RawTxHex)
	if err != nil {
		return nil, err
	}
	wireTx := &wire.MsgTx{}
	txReader := bytes.NewReader(rawTx)

	if err := wireTx.Deserialize(txReader); err != nil {
		return nil, err
	}
	var destAddresses []btcutil.Address
	chainParams := &chaincfg.MainNetParams
	for _, txOut := range wireTx.TxOut {
		_, outAddresses, _, err :=
			txscript.ExtractPkScriptAddrs(txOut.PkScript, chainParams)
		if err != nil {
			return nil, err
		}

		destAddresses = append(destAddresses, outAddresses...)
	}

	dest := make([]string, 0, len(destAddresses))
	for _, destAddress := range destAddresses {
		dest = append(dest, destAddress.EncodeAddress())
	}
	return dest, nil
}

func subscribeTransactionsOnce(ctx context.Context, c lnrpc.LightningClient) error {
	transactionStream, err := c.SubscribeTransactions(ctx, &lnrpc.GetTransactionsRequest{})
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
		t.DestAddresses, err = destAddresses(t)
		if err != nil {
			log.Printf("transactionStream - Error in destAddresses: %v", err)
		}
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
	registrationKey, err := doRegisterTransacionConfirmation(txID, token, notifyType)
	if err != nil {
		return err
	}
	err = setKeyExpiration(registrationKey, transactionNotificationExpiry)
	return err
}

func doRegisterTransacionConfirmation(txID, token, notifyType string) (string, error) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	registrationKey := fmt.Sprintf("tx-notify-%v", txID)
	registrationData := map[string]string{"token": token, "type": notifyType}
	marshalled, err := json.Marshal(registrationData)
	if err != nil {
		return "", err
	}
	_, err = redisConn.Do("SADD", registrationKey, string(marshalled))
	if err != nil {
		return "", err
	}

	return registrationKey, nil
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
			go func() {
				err := notifyAlertMessage(
					notificationTypes[notificationType]["title"],
					notificationTypes[notificationType]["body"],
					map[string]string{}, notificationToken)

				if err != nil {
					log.Printf("Error sending transaction confirmation %v", err)
				}
			}()
		}

		if len(registrations) < 10 {
			break
		}
	}
	return nil
}

func handleTransactionAddreses(tx *lnrpc.Transaction) error {
	//log.Printf("t:%#v", tx)
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
				err = handleTransactionAddress(tx, i, redisConn)
				if err != nil {
					return err
				}
				go notifyClientTransaction(tx, i, "Action Required", "Breez", "Received funds are now confirmed. Please open the app to complete your transaction.", true)
				break // There is only one address concerning us per transaction
			} else {
				_, err := redisConn.Do("HMSET", "input-address:"+tx.DestAddresses[i],
					"utx:TxHash", tx.TxHash,
					"utx:Amount", tx.Amount,
				)
				if err != nil {
					log.Println("handleTransactionAddreses error:", err)
					return err
				}
				amt := strconv.FormatInt(tx.Amount, 10)
				go notifyClientTransaction(tx, i, "Unconfirmed transaction", "Breez", "Breez is waiting for "+amt+" sat to be confirmed.", false)
				break
			}
		}
	}
	return nil
}

func notifyClientTransaction(tx *lnrpc.Transaction, index int, msg, title, body string, delete bool) {
	key := tx.TxHash + "-notification"
	_, _, _ = txNotificationGroup.Do(key, func() (interface{}, error) {
		tokens, data, err := getTxNotificationData(tx, index, msg)
		if err != nil {
			return nil, nil
		}

		for _, tok := range tokens {
			err = notifyAlertMessage(title, body, data, tok)
			log.Println("Error in send:", err)
			unregistered := err != nil && isUnregisteredError(err)
			if unregistered || delete {
				unregisterTxNotification(tx, index, tok)
			}
		}
		return nil, nil
	})
}

func getTxNotificationData(tx *lnrpc.Transaction, index int, msg string) ([]string, map[string]string, error) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	tokens, err := redis.Strings(redisConn.Do("SMEMBERS", "input-address-notification:"+tx.DestAddresses[index]))
	if err != nil {
		log.Println("notifyUnconfirmed error:", err)
		return nil, nil, err
	}
	data := map[string]string{
		"msg":     msg,
		"tx":      tx.TxHash,
		"address": tx.DestAddresses[index],
		"value":   strconv.FormatInt(tx.Amount, 10),
	}

	return tokens, data, nil
}

func unregisterTxNotification(tx *lnrpc.Transaction, index int, tok string) {
	redisConn := redisPool.Get()
	defer redisConn.Close()

	_, err := redisConn.Do("SREM", "input-address-notification:"+tx.DestAddresses[index], tok)
	if err != nil {
		log.Printf("Error in notifyClientTransaction (SREM); set:%v member:%v error:%v", "input-address-notification:"+tx.DestAddresses[index], tok, err)
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

func handleTransactionAddress(tx *lnrpc.Transaction, index int, redisConn redis.Conn) error {
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
