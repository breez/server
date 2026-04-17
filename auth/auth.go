package auth

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type providerCtxKeyType string

const providerCtxKey providerCtxKeyType = "provider"

type certCtxKeyType string

const certCtxKey certCtxKeyType = "cert"

func GetCert(r *http.Request) *x509.Certificate {
	cert, _ := r.Context().Value(certCtxKey).(*x509.Certificate)
	return cert
}

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

func AuthenticatedHandler(prefix string, h http.Handler, u *url.URL) func(http.ResponseWriter, *http.Request) {
	CACertBlock, _ := pem.Decode([]byte(os.Getenv("BREEZ_CA_CERT")))
	if CACertBlock == nil {
		log.Fatal("Breez CA cert invalid")
	}
	CACert, err := x509.ParseCertificate(CACertBlock.Bytes)
	if err != nil {
		log.Fatal("Cannot parse CA cert:", err)
	}

	rootPool := x509.NewCertPool()
	rootPool.AddCert(CACert)

	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("authorization")
		if len(authHeader) < 8 || !strings.HasPrefix(authHeader, "Bearer ") {
			log.Printf("No bearer data in authorization header: %v", authHeader)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		apiKey := authHeader[7:]
		block, err := base64.StdEncoding.DecodeString(apiKey)
		if err != nil {
			log.Printf("base64.StdEncoding.DecodeString(apiKey) [%v] error: %v", apiKey, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cert, err := x509.ParseCertificate(block)
		if err != nil {
			log.Printf("Cannot parse cert: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		chains, err := cert.Verify(x509.VerifyOptions{
			Roots: rootPool,
		})
		if err != nil {
			log.Printf("cert.Verify error: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(chains) != 1 || len(chains[0]) != 2 || !chains[0][0].Equal(cert) || !chains[0][1].Equal(CACert) {
			log.Printf("cert verification error")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if u != nil {
			r.Host = u.Host
		}
		r = r.WithContext(context.WithValue(r.Context(), certCtxKey, cert))
		http.StripPrefix(prefix, h).ServeHTTP(w, r)
	}
}

func JWTHandler(w http.ResponseWriter, r *http.Request) {
	cert := GetCert(r)
	if cert == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	keyPEM := os.Getenv("JWT_PRIVATE_KEY")
	if keyPEM == "" {
		log.Printf("JWT_PRIVATE_KEY not set")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	privateKey, err := jwt.ParseECPrivateKeyFromPEM([]byte(keyPEM))
	if err != nil {
		log.Printf("jwt.ParseECPrivateKeyFromPEM error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"aud": "spark-so",
		"iss": fmt.Sprintf("breez-%s", cert.SerialNumber),
		"iat": now.Unix(),
		"exp": now.Add(7 * 24 * time.Hour).Unix(),
	}
	if len(cert.Subject.Organization) > 0 {
		claims["sub"] = cert.Subject.Organization[0]
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := token.SignedString(privateKey)
	if err != nil {
		log.Printf("token.SignedString error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": signed})
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
