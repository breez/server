package liquid

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
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
	return func(w http.ResponseWriter, r *http.Request) {
		swapID := r.Header.Get("Swap-ID")
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
		r.Host = u.Host
		http.StripPrefix(prefix, p).ServeHTTP(w, r)
	}
}

func MempoolHandler(prefix string, p *httputil.ReverseProxy, u *url.URL) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Host = u.Host
		http.StripPrefix(prefix, p).ServeHTTP(w, r)
	}
}
