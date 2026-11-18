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

var (
	breezCACert *x509.Certificate
	breezCAPool *x509.CertPool
	breezCRL    map[string]map[string]struct{}
)

func init() {
	CACertBlock, _ := pem.Decode([]byte(os.Getenv("BREEZ_CA_CERT")))
	if CACertBlock == nil {
		log.Fatal("Breez CA cert invalid")
	}
	var err error
	breezCACert, err = x509.ParseCertificate(CACertBlock.Bytes)
	if err != nil {
		log.Fatal("Cannot parse CA cert:", err)
	}

	breezCAPool = x509.NewCertPool()
	breezCAPool.AddCert(breezCACert)

	breezCRL = make(map[string]map[string]struct{})
	crlJSON := os.Getenv("BREEZ_CA_CRL")
	if crlJSON != "" {
		var raw map[string]string
		if err := json.Unmarshal([]byte(crlJSON), &raw); err != nil {
			log.Fatal("Cannot parse BREEZ_CA_CRL:", err)
		}
		for path, list := range raw {
			serials := make(map[string]struct{})
			for c := range strings.SplitSeq(list, ",") {
				if c != "" {
					serials[c] = struct{}{}
				}
			}
			breezCRL[path] = serials
		}
	}
}

func isAncestorPath(ancestor, path string) bool {
	if ancestor == "" {
		return false
	}
	if ancestor == "/" {
		return true
	}
	if strings.HasSuffix(ancestor, "/") {
		return strings.HasPrefix(path, ancestor)
	}
	return path == ancestor || strings.HasPrefix(path, ancestor+"/")
}

func isRevoked(serial, path string) bool {
	for crlPath, serials := range breezCRL {
		if isAncestorPath(crlPath, path) {
			if _, ok := serials[serial]; ok {
				return true
			}
		}
	}
	return false
}

func verifyClientCert(cert *x509.Certificate, path string) error {
	chains, err := cert.Verify(x509.VerifyOptions{
		Roots: breezCAPool,
	})
	if err != nil {
		return err
	}
	if len(chains) != 1 || len(chains[0]) != 2 || !chains[0][0].Equal(cert) || !chains[0][1].Equal(breezCACert) {
		return fmt.Errorf("cert verification error")
	}
	if isRevoked(cert.SerialNumber.String(), path) {
		return fmt.Errorf("certificate revoked: %s", cert.SerialNumber)
	}
	return nil
}

type serialCtxKeyType string

const serialCtxKey serialCtxKeyType = "client-serial"

func ClientSerial(ctx context.Context) (string, bool) {
	serial, ok := ctx.Value(serialCtxKey).(string)
	return serial, ok
}

func UnaryClientCertSerial(prefix string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if !strings.HasPrefix(info.FullMethod, prefix) {
			return handler(ctx, req)
		}
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			for _, auth := range md.Get("authorization") {
				if !strings.HasPrefix(auth, "Bearer ") {
					continue
				}
				block, err := base64.StdEncoding.DecodeString(auth[7:])
				if err != nil {
					log.Printf("UnaryClientCertSerial: base64 decode error: %v", err)
					continue
				}
				cert, err := x509.ParseCertificate(block)
				if err != nil {
					log.Printf("UnaryClientCertSerial: parse cert error: %v", err)
					continue
				}
				if err := verifyClientCert(cert, info.FullMethod); err != nil {
					log.Printf("UnaryClientCertSerial: verify error: %v", err)
					return nil, status.Errorf(codes.PermissionDenied, "%v", err)
				}
				ctx = context.WithValue(ctx, serialCtxKey, cert.SerialNumber.String())
				break
			}
		}
		return handler(ctx, req)
	}
}

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
		if err := verifyClientCert(cert, "/"); err != nil {
			log.Printf("verifyClientCert error: %v", err)
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

func CRLHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(os.Getenv("BREEZ_CA_CRL")))
}

func JWTHandler(w http.ResponseWriter, r *http.Request) {
	cert := GetCert(r)
	if cert == nil {
		log.Printf("JWTHandler: no cert")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	keyPEM := os.Getenv("JWT_PRIVATE_KEY")
	if keyPEM == "" {
		log.Printf("JWTHandler: JWT_PRIVATE_KEY not set")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	privateKey, err := jwt.ParseECPrivateKeyFromPEM([]byte(keyPEM))
	if err != nil {
		log.Printf("JWTHandler: jwt.ParseECPrivateKeyFromPEM error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"aud": []string{"spark-so", "spark-ssp"},
		"iss": "breez.technology",
		"iat": now.Unix(),
		"exp": now.Add(7 * 24 * time.Hour).Unix(),
	}
	if len(cert.Subject.Organization) > 0 {
		claims["sub"] = fmt.Sprintf("%s [%s]", cert.Subject.Organization[0], cert.SerialNumber)
	} else {
		claims["sub"] = fmt.Sprintf("[%s]", cert.SerialNumber)
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	signed, err := token.SignedString(privateKey)
	if err != nil {
		log.Printf("JWTHandler: token.SignedString error: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	log.Printf("JWTHandler: sent jwt: %s (serial: %s) to X-Forwarded-For: %s", signed, cert.SerialNumber, r.Header.Get("X-Forwarded-For"))
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
