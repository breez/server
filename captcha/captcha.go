package captcha

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/breez/server/breez"
	"github.com/mojocn/base64Captcha"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	authType = "Captcha-v1"
)

func UnaryCaptchaAuth(prefix, config string) grpc.UnaryServerInterceptor {
	if config == "" {
		return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
			return handler(ctx, req)
		}
	}
	var captchaCfg base64Captcha.ConfigCharacter
	err := json.Unmarshal([]byte(config), &captchaCfg)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !strings.HasPrefix(info.FullMethod, prefix) {
			return handler(ctx, req)
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			for _, auth := range md.Get("authorization") {
				if strings.HasPrefix(auth, authType+" ") {
					if c := strings.Split(auth[len(authType)+1:], " "); len(c) > 1 && base64Captcha.VerifyCaptcha(c[0], c[1]) {
						return handler(ctx, req)
					}

				}
			}
		}
		id, captcha := base64Captcha.GenerateCaptcha("", captchaCfg)
		cap := captcha.BinaryEncoding()
		s, _ := status.New(codes.PermissionDenied, "Not authorized").WithDetails(&breez.Captcha{Id: id, Image: cap})
		return nil, s.Err()
	}
}
