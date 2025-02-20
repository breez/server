package liquid

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/breez/server/breez"
)

func checkSwapId(fc *fastcache.Cache, apiURL, swapID string, body []byte) error {
	c := &http.Client{Timeout: 10 * time.Second}
	r, err := c.Get(apiURL + swapID)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return errors.New("bad swap id")
	}
	s := struct {
		Status string `json:"status"`
	}{}
	err = json.NewDecoder(r.Body).Decode(&s)
	if err != nil {
		return err
	}
	if s.Status != "swap.created" && s.Status != "invoice.set" {
		return errors.New("bad status")
	}
	h := sha256.Sum256(body)
	if hc, exists := fc.HasGet(nil, []byte(swapID)); exists {
		if bytes.Equal(h[:], hc) {
			return nil
		} else {
			return errors.New("duplicate swapid")
		}
	}
	fc.Set([]byte(swapID), h[:])
	return nil
}

func BroadcastHandler(chainApiServers []*breez.ChainApiServersReply_ChainAPIServer, prefix string, p *httputil.ReverseProxy, u *url.URL) func(http.ResponseWriter, *http.Request) {
	apiURL := ""
	for _, cs := range chainApiServers {
		if cs.ServerType == "BOLTZ_SWAPPER" {
			baseURL := cs.ServerBaseUrl
			u, err := url.Parse(baseURL)
			if err != nil {
				log.Printf("Liquid: Error %v when parsing swapper URL: %v", err, baseURL)
				continue
			}
			u.RawQuery = ""
			u.Path = "/v2/swap/"
			apiURL = u.String()
			break
		}
	}
	if apiURL == "" {
		log.Fatal("Liquid: No api URL found")
	}
	fc := fastcache.New(100_000_000)

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
		if len(authHeader) > 7 && strings.HasPrefix(authHeader, "Bearer ") {
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
		} else {
			swapID := r.Header.Get("Swap-Id")
			if swapID == "" {
				log.Printf("Liquid: No swapID")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 100_000))
			if err != nil {
				log.Printf("Liquid: error reading body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			err = checkSwapId(fc, apiURL, swapID, body)
			if err != nil {
				log.Printf("Liquid: error when checking swapID=%v : %v", swapID, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			r.Body = io.NopCloser(bytes.NewBuffer(body))
		}

		r.Host = u.Host
		http.StripPrefix(prefix, p).ServeHTTP(w, r)
	}
}

func AuthenticatedHandler(prefix string, p *httputil.ReverseProxy, u *url.URL) func(http.ResponseWriter, *http.Request) {
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

		r.Host = u.Host
		http.StripPrefix(prefix, p).ServeHTTP(w, r)
	}
}
