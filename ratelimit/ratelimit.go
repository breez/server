package ratelimit

import (
	"context"
	"log"
	"net"

	"github.com/gomodule/redigo/redis"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func getIP(ctx context.Context, proxyAddress string) string {
	p, ok := peer.FromContext(ctx)
	if !ok {
		log.Printf("peer error.")
	}
	var srcIP string
	switch addr := p.Addr.(type) {
	case *net.UDPAddr:
		srcIP = addr.IP.String()
	case *net.TCPAddr:
		srcIP = addr.IP.String()
	}
	if srcIP == proxyAddress {
		md, ok := metadata.FromIncomingContext(ctx)
		//log.Println(ok, md)
		if ok {
			realIP := md["x-forwarded-for"]
			if len(realIP) > 0 {
				srcIP = realIP[0]
			}
		}
	}
	return srcIP
}

/*
limit: X-RateLimit-Limit
remaining: X-RateLimit-Remaining
retryAfter: Retry-After
reset: X-RateLimit-Reset
*/
func getThrottle(redisPool *redis.Pool, key string, maxBurst, tokens, seconds uint) (blocked bool, limit, remaining, retryAfter, reset int) {
	redisConn := redisPool.Get()
	defer redisConn.Close()
	r, err := redis.Ints(redisConn.Do("CL.THROTTLE", key, maxBurst, tokens, seconds))
	if err == redis.ErrNil {
		return //Don't block
	}
	if err != nil {
		log.Printf("getThrottle error: %v", err)
	}
	log.Println(key, r)
	blocked = r[0] == 1
	limit = r[1]
	remaining = r[2]
	retryAfter = r[3]
	reset = r[4]
	return
}

func UnaryRateLimiter(redisPool *redis.Pool, prefix, fullMethod string, maxBurst, tokens, seconds uint) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if info.FullMethod != fullMethod {
			return handler(ctx, req)
		}
		blocked, limit, remaining, retryAfter, reset := getThrottle(redisPool, prefix+"/method"+info.FullMethod, maxBurst, tokens, seconds)
		if blocked {
			_, _, _, _ = limit, remaining, retryAfter, reset //Need to add headers
			return nil, status.Errorf(codes.ResourceExhausted, "%s is rejected by ratelimit, please retry later.", info.FullMethod)
		}
		return handler(ctx, req)
	}
}

func PerIPUnaryRateLimiter(redisPool *redis.Pool, proxyAddress, prefix, fullMethod string, maxBurst, tokens, seconds uint) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if info.FullMethod != fullMethod {
			return handler(ctx, req)
		}
		srcIP := getIP(ctx, proxyAddress)
		blocked, limit, remaining, retryAfter, reset := getThrottle(redisPool, prefix+"/ip/"+srcIP+"/method"+info.FullMethod, maxBurst, tokens, seconds)
		if blocked {
			_, _, _, _ = limit, remaining, retryAfter, reset //Need to add headers
			return nil, status.Errorf(codes.ResourceExhausted, "%s is rejected by ratelimit, please retry later.", info.FullMethod)
		}
		return handler(ctx, req)
	}
}
