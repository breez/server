package signer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"

	"github.com/breez/server/breez"
)

type Server struct {
	breez.UnimplementedSignerServer
}

func (s *Server) SignUrl(ctx context.Context, in *breez.SignUrlRequest) (*breez.SignUrlResponse, error) {
	if in.BaseUrl != "https://buy.moonpay.io" {
		return nil, fmt.Errorf("invalid URL")
	}
	h := hmac.New(sha256.New, []byte(os.Getenv("MOONPAY_SECRET")))
	h.Write([]byte(in.QueryString))
	var out breez.SignUrlResponse
	out.FullUrl = in.BaseUrl + in.QueryString + "&signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil)))
	return &out, nil
}
