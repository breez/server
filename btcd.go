package main

import (
	"log"
	"os"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/rpcclient"
	"github.com/gomodule/redigo/redis"
)

func onTxAcceptedVerbose(txDetails *btcjson.TxRawResult) {
	if redisPool == nil {
		return
	}

	redisConn := redisPool.Get()
	defer redisConn.Close()
	for _, vout := range txDetails.Vout {
		for _, a := range vout.ScriptPubKey.Addresses {
			id, err := redis.String(redisConn.Do("GET", "address:"+a))
			if err == nil { // exists
				txkey := "transaction:" + txDetails.Hash + ":address:" + a
				redisConn.Do("HMSET", txkey, "clientID", id, "tx", txDetails.Hash, "address", a, "value", vout.Value)
				log.Println("HMSET", txkey, "clientID", id, "tx", txDetails.Hash, "address", a, "value", vout.Value)
				redisConn.Do("EXPIRE", txkey, 24*3600)
				redisConn.Do("SADD", "transactions:"+a, txkey)
				redisConn.Do("EXPIRE", "transactions:"+a, 24*3600)
				redisConn.Do("LPUSH", "notifications", a)
			} else if err != redis.ErrNil {
				log.Println("Error in redis.Get:", err)
				return
			}
		}
	}
}

func btcdConnect() error {
	ntfnHandlers := rpcclient.NotificationHandlers{
		OnTxAcceptedVerbose: onTxAcceptedVerbose,
	}

	connCfg := &rpcclient.ConnConfig{
		Host:         os.Getenv("BTCD_HOST"),
		Endpoint:     "ws",
		User:         os.Getenv("BTCD_USER"),
		Pass:         os.Getenv("BTCD_PASS"),
		Certificates: []byte(strings.Replace(os.Getenv("BTCD_CERT"), "\\n", "\n", -1)),
	}

	btcdClient, err := rpcclient.New(connCfg, &ntfnHandlers)
	if err != nil {
		return err
	}

	err = btcdClient.NotifyNewTransactions(true)
	if err != nil {
		return err
	}

	return nil
}
