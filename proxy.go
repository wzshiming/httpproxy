package httpproxy

import (
	"context"
	"fmt"
	"io"
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
	Logger Logger
	// BytesPool getting and returning temporary bytes for use by io.CopyBuffer
	BytesPool BytesPool
}

type Logger interface {
	Println(v ...interface{})
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

func (p *ProxyHandler) proxyConnect(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		e := "not support"
		if p.Logger != nil {
			p.Logger.Println(e)
		}
		http.Error(w, e, http.StatusInternalServerError)
		return
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

	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	} else {
		w.WriteHeader(http.StatusOK)
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		e := err.Error()
		if p.Logger != nil {
			p.Logger.Println(e)
		}
		http.Error(w, e, http.StatusInternalServerError)
		return
	}

	var buf1, buf2 []byte
	if p.BytesPool != nil {
		buf1 = p.BytesPool.Get()
		buf2 = p.BytesPool.Get()
		defer func() {
			p.BytesPool.Put(buf1)
			p.BytesPool.Put(buf2)
		}()
	} else {
		buf1 = make([]byte, 32*1024)
		buf2 = make([]byte, 32*1024)
	}
	err = tunnel(r.Context(), targetConn, clientConn, buf1, buf2)
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
