package main

import (
	"log"
	"os"
	"time"

	"github.com/NaySoftware/go-fcm"
	"github.com/gomodule/redigo/redis"
)

func notify() {
	if redisPool == nil {
		return
	}

	redisConn := redisPool.Get()
	defer redisConn.Close()
	for {
		t, err := redis.Strings(redisConn.Do("BRPOP", "notifications", 10))
		if err == redis.ErrNil {
			time.Sleep(1 * time.Second)
			continue
		} else {
			if err != nil {
				log.Println("Error in BRPOP:", err)
				continue
			}
			m, err := redis.StringMap(redisConn.Do("HGETALL", t[1]))
			if err == nil {
				data := map[string]string{
					"msg":     "Mempool transaction",
					"tx":      m["tx"],
					"address": m["address"],
					"value":   m["value"],
				}
				fcmClient := fcm.NewFcmClient(os.Getenv("FCM_KEY"))
				status, err := fcmClient.NewFcmRegIdsMsg([]string{m["clientID"]}, data).
					SetPriority(fcm.Priority_HIGH).
					Send()

				status.PrintResults()
				if err != nil {
					log.Println("Error in send:", err)
				}
			} else if err != redis.ErrNil {
				log.Println("Error in HMGET")
			}
		}
	}
}
