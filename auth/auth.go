package auth

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type providerCtxKeyType string

const providerCtxKey providerCtxKeyType = "provider"

func GetProvider(ctx context.Context) *string {
	provider, ok := ctx.Value(providerCtxKey).(*string)
	if !ok || provider == nil {
		log.Printf("GetProvider(): no provider found")
		return nil
	}
	log.Printf("context provider string value: %#v", *provider)
	return provider
}

func GetHeaderKeys(ctx context.Context) []string {
	var keys []string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		for _, auth := range md.Get("authorization") {
			if strings.HasPrefix(auth, "Bearer ") {
				if len(auth) > 7 {
					keys = append(keys, auth[7:])
				}
			}
		}
	}
	return keys
}

func UnaryAuth(prefix, token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !strings.HasPrefix(info.FullMethod, prefix) {
			return handler(ctx, req)
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			for _, auth := range md.Get("authorization") {
				if auth == "Bearer "+token {
					return handler(ctx, req)
				}
			}
		}
		return nil, status.Errorf(codes.PermissionDenied, "Not authorized")
	}
}

func UnaryMultiAuth(prefix, jsonTokens string) grpc.UnaryServerInterceptor {
	tokens := make(map[string]string)
	err := json.Unmarshal([]byte(jsonTokens), &tokens)
	if err != nil {
		log.Printf("json.Unmarshal(%v) error: %v", jsonTokens, err)
		return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
			return nil, status.Errorf(codes.PermissionDenied, "Not authorized")
		}
	}
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !strings.HasPrefix(info.FullMethod, prefix) {
			return handler(ctx, req)
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			for _, auth := range md.Get("authorization") {
				if strings.HasPrefix(auth, "Bearer ") {
					if provider, ok := tokens[auth[7:]]; ok {
						return handler(context.WithValue(ctx, providerCtxKey, &provider), req)
					}
				}

			}
		}
		return nil, status.Errorf(codes.PermissionDenied, "Not authorized")
	}
}
