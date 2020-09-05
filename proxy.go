package httpproxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
)

// ProxyHandler proxy handler
type ProxyHandler struct {
	// Client  is used without the connect method
	Client *http.Client
	// ProxyDial specifies the optional proxyDial function for
	// establishing the transport connection.
	ProxyDial func(context.Context, string, string) (net.Conn, error)
	// Authentication is proxy authentication
	Authentication Authentication
	// NotFound Not proxy requests
	NotFound http.Handler
	// Logger error log
	Logger *log.Logger
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
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
		handle := p.NotFound
		if handle == nil {
			handle = http.HandlerFunc(http.NotFound)
		}
		handle.ServeHTTP(w, r)
	}
}

func (p *ProxyHandler) proxyOther(w http.ResponseWriter, r *http.Request) {
	r = r.Clone(r.Context())
	r.RequestURI = ""

	resp, err := p.client().Do(r)
	if err != nil {
		e := err.Error()
		if p.Logger != nil {
			p.Logger.Println(e)
		}
		http.Error(w, e, http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	header := w.Header()
	for k, v := range resp.Header {
		header[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	if err != nil && p.Logger != nil {
		p.Logger.Println(err)
	}
	return
}

var HTTP200 = []byte("HTTP/1.1 200 Connection Established\r\n\r\n")

func (p *ProxyHandler) proxyConnect(w http.ResponseWriter, r *http.Request) {
	var clientConn io.ReadWriteCloser

	switch t := w.(type) {
	default:
		e := "not support"
		if p.Logger != nil {
			p.Logger.Println(e)
		}
		http.Error(w, e, http.StatusInternalServerError)
		return
	case http.Hijacker:
		conn, _, err := t.Hijack()
		if err != nil {
			e := err.Error()
			if p.Logger != nil {
				p.Logger.Println(e)
			}
			http.Error(w, e, http.StatusInternalServerError)
			return
		}
		_, err = conn.Write(HTTP200)
		if err != nil {
			e := err.Error()
			if p.Logger != nil {
				p.Logger.Println(e)
			}
			http.Error(w, e, http.StatusInternalServerError)
			return
		}
		clientConn = conn
	case http.Flusher:
		t.Flush()
		clientConn = &flushWriter{w, r.Body}
	}

	targetConn, err := p.proxyDial(r.Context(), "tcp", r.URL.Host)
	if err != nil {
		e := fmt.Sprintf("dial %q failed: %v", r.URL.Host, err)
		if p.Logger != nil {
			p.Logger.Println(e)
		}
		http.Error(w, e, http.StatusInternalServerError)
		return
	}

	err = tunnel(r.Context(), targetConn, clientConn)
	if err != nil && p.Logger != nil {
		p.Logger.Println(err)
	}
	return
}

func (p *ProxyHandler) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: p.proxyDial,
		},
	}
}

func (p *ProxyHandler) proxyDial(ctx context.Context, network, address string) (net.Conn, error) {
	proxyDial := p.ProxyDial
	if proxyDial == nil {
		var dialer net.Dialer
		proxyDial = dialer.DialContext
	}
	return proxyDial(ctx, network, address)
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
