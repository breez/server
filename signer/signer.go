package signer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/breez/server/auth"
	"github.com/breez/server/breez"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	breez.UnimplementedSignerServer
}

func canonicalizeIP(ipStr string) string {
	ipStr = strings.TrimSpace(strings.Split(ipStr, ",")[0])
	if host, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = host
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

func hashIP(ip, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(ip))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func allowedIPAddresses(ipv4, ipv6, secret string) string {
	var hashes []string
	if ipv4 != "" {
		if ip := canonicalizeIP(ipv4); ip != "" {
			hashes = append(hashes, url.QueryEscape(hashIP(ip, secret)))
		}
	}
	if ipv6 != "" {
		if ip := canonicalizeIP(ipv6); ip != "" {
			hashes = append(hashes, url.QueryEscape(hashIP(ip, secret)))
		}
	}
	return strings.Join(hashes, ",")
}

func externalTransactionID(ctx context.Context, pubkey string) string {
	var parts []string
	if serial, ok := auth.ClientSerial(ctx); ok {
		parts = append(parts, serial)
	} else {
		parts = append(parts, "")
	}
	parts = append(parts, pubkey)
	parts = append(parts, strconv.FormatInt(time.Now().UnixNano(), 10))
	return strings.Join(parts, ",")
}

func (s *Server) SignUrl(ctx context.Context, in *breez.SignUrlRequest) (*breez.SignUrlResponse, error) {
	if _, ok := auth.ClientSerial(ctx); !ok {
		return nil, status.Errorf(codes.PermissionDenied, "client certificate required")
	}
	if in.BaseUrl != "https://buy.moonpay.io" {
		return nil, fmt.Errorf("invalid URL")
	}
	secret := os.Getenv("MOONPAY_SECRET")
	queryString := in.QueryString

	if allowed := allowedIPAddresses(in.Ipv4, in.Ipv6, secret); allowed != "" {
		queryString = queryString + "&allowedIpAddress=" + allowed
	}

	queryString = queryString + "&externalTransactionId=" + url.QueryEscape(externalTransactionID(ctx, in.Pubkey))

	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(queryString))
	var out breez.SignUrlResponse
	out.FullUrl = in.BaseUrl + queryString + "&signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil)))
	return &out, nil
}
