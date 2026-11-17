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
	"strings"

	"github.com/breez/server/breez"
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

func (s *Server) SignUrl(ctx context.Context, in *breez.SignUrlRequest) (*breez.SignUrlResponse, error) {
	if in.BaseUrl != "https://buy.moonpay.io" {
		return nil, fmt.Errorf("invalid URL")
	}
	secret := os.Getenv("MOONPAY_SECRET")
	queryString := in.QueryString

	if allowed := allowedIPAddresses(in.Ipv4, in.Ipv6, secret); allowed != "" {
		queryString = queryString + "&allowedIpAddress=" + allowed
	}

	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(queryString))
	var out breez.SignUrlResponse
	out.FullUrl = in.BaseUrl + queryString + "&signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil)))
	return &out, nil
}
