package httpproxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

func (p *ProxyHandler) init() {
	if p.ProxyDial == nil {
		var dialer net.Dialer
		p.ProxyDial = dialer.DialContext
	}
	if p.Client == nil {
		p.Client = &http.Client{
			Transport: &http.Transport{
				DialContext: p.ProxyDial,
			},
		}
	}
	if p.NotFound == nil {
		p.NotFound = http.HandlerFunc(http.NotFound)
	}
}

func (p *ProxyHandler) proxyOther(w http.ResponseWriter, r *http.Request) {
	r.RequestURI = ""

	resp, err := p.Client.Do(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	header := w.Header()
	for k, v := range resp.Header {
		header[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	return
}

func (p *ProxyHandler) proxyConnect(w http.ResponseWriter, r *http.Request) {

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not allowed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	targetConn, err := p.ProxyDial(r.Context(), "tcp", r.URL.Host)
	if err != nil {
		http.Error(w, fmt.Sprintf("dial(%q) failed: %v", r.URL.Host, err), http.StatusInternalServerError)
		return
	}

	closeConn := func() {
		clientConn.Close()
		targetConn.Close()
	}
	var once sync.Once
	go func() {
		var buf [32 * 1024]byte
		io.CopyBuffer(targetConn, clientConn, buf[:])
		once.Do(closeConn)
	}()
	go func() {
		var buf [32 * 1024]byte
		io.CopyBuffer(clientConn, targetConn, buf[:])
		once.Do(closeConn)
	}()
	return
}

// ProxyHandler proxy handler
type ProxyHandler struct {
	once           sync.Once
	Client         *http.Client
	ProxyDial      func(context.Context, string, string) (net.Conn, error)
	Authentication Authentication
	NotFound       http.Handler
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.Authentication != nil && !p.Authentication.Auth(w, r) {
		return
	}
	p.once.Do(p.init)

	switch {
	case r.Method == "CONNECT":
		p.proxyConnect(w, r)
	case r.URL.Host != "":
		p.proxyOther(w, r)
	default:
		p.NotFound.ServeHTTP(w, r)
	}
}
