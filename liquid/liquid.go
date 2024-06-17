package liquid

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

func BroadcastHandler(prefix string, p *httputil.ReverseProxy, u *url.URL) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		swapInfo := r.Header.Get("Swap-Info")
		_ = swapInfo //Add checks
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
