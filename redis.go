package main

import (
	"os"
	"strconv"
	"time"

	"github.com/gomodule/redigo/redis"
)

var (
	redisPool *redis.Pool
)

func redisConnect() error {
	db, err := strconv.Atoi(os.Getenv("REDIS_DB"))
	if err != nil {
		return err
	}
	redisPool = &redis.Pool{
		MaxIdle:     3,
		IdleTimeout: 240 * time.Second,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", os.Getenv("REDIS_URL"), redis.DialDatabase(db))
			if err != nil {
				return nil, err
			}
			return c, err
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}

	return nil
}

func updateKeyFields(key string, fields map[string]string) error {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	var args []interface{}
	args = append(args, key)
	for k, value := range fields {
		args = append(args, k)
		args = append(args, value)
	}
	_, err := redisConn.Do("HMSET", args...)
	return err
}

func getKeyFields(key string) (map[string]string, error) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	res, err := redis.StringMap(redisConn.Do("HGETALL", key))
	if err == redis.ErrNil {
		return nil, nil
	}
	return res, err
}

func deleteKey(key string) error {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	_, err := redisConn.Do("DEL", key)
	return err
}

func keyExists(key string) (bool, error) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	return redis.Bool(redisConn.Do("EXISTS", key))
}

func setKeyExpiration(key string, seconds int64) error {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	_, err := redis.Bool(redisConn.Do("EXPIRE", key, seconds))
	return err
}

func getKeyExpiration(key string) (int64, error) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	ttl, err := redis.Int64(redisConn.Do("TTL", key))
	return ttl, err
}
