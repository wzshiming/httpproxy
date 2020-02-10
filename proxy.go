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
	var clientConn io.ReadWriteCloser

	switch t := w.(type) {
	default:
		http.Error(w, "not support", http.StatusInternalServerError)
		return
	case http.Hijacker:
		w.WriteHeader(http.StatusOK)
		conn, _, err := t.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		clientConn = conn
	case http.Flusher:
		t.Flush()
		clientConn = &flushWriter{w, r.Body}
	}

	targetConn, err := p.ProxyDial(r.Context(), "tcp", r.URL.Host)
	if err != nil {
		http.Error(w, fmt.Sprintf("dial %q failed: %v", r.URL.Host, err), http.StatusInternalServerError)
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

	var buf [32 * 1024]byte
	io.CopyBuffer(clientConn, targetConn, buf[:])
	once.Do(closeConn)
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
	p.once.Do(p.init)
	switch {
	case r.Method == "CONNECT":
		if p.Authentication != nil && !p.Authentication.Auth(w, r) {
			return
		}
		p.proxyConnect(w, r)
	case r.URL.Host != "":
		if p.Authentication != nil && !p.Authentication.Auth(w, r) {
			return
		}
		p.proxyOther(w, r)
	default:
		p.NotFound.ServeHTTP(w, r)
	}
}

type flushWriter struct {
	w io.Writer
	io.ReadCloser
}

func (fw flushWriter) Write(p []byte) (n int, err error) {
	n, err = fw.w.Write(p)
	fw.w.(http.Flusher).Flush()
	return
}
