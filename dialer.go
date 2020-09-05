package httpproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// NewDialer is create a new HTTP CONNECT connection
func NewDialer(addr string) (*Dialer, error) {
	d := &Dialer{}

	proxy, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	d.Userinfo = proxy.User
	switch proxy.Scheme {
	default:
		return nil, fmt.Errorf("unsupported protocol '%s'", proxy.Scheme)
	case "https":
		hostname := proxy.Hostname()
		host := proxy.Host
		port := proxy.Port()
		if port == "" {
			port = "443"
			host = net.JoinHostPort(hostname, port)
		}
		d.Proxy = host
		d.TLSClientConfig = &tls.Config{
			ServerName: hostname,
		}
	case "http":
		host := proxy.Host
		port := proxy.Port()
		if port == "" {
			port = "443"
			host = net.JoinHostPort(proxy.Hostname(), port)
		}
		d.Proxy = host
	}
	return d, nil
}

// Dialer holds HTTP CONNECT options.
type Dialer struct {
	// ProxyDial specifies the optional dial function for
	// establishing the transport connection.
	ProxyDial func(context.Context, string, string) (net.Conn, error)

	// TLSClientConfig specifies the TLS configuration to use with
	// tls.Client.
	// If nil, the TLS is not used.
	// If non-nil, HTTP/2 support may not be enabled by default.
	TLSClientConfig *tls.Config

	// ProxyHeader optionally specifies headers to send to
	// proxies during CONNECT requests.
	ProxyHeader http.Header

	// Proxy proxy server address
	Proxy string

	// Userinfo use userinfo authentication if not empty
	Userinfo *url.Userinfo
}

func (d *Dialer) proxyDial(ctx context.Context, network string, address string) (net.Conn, error) {
	proxyDial := d.ProxyDial
	if proxyDial == nil {
		var dialer net.Dialer
		proxyDial = dialer.DialContext
	}

	rawConn, err := proxyDial(ctx, network, address)
	if err != nil {
		return nil, err
	}

	config := d.TLSClientConfig
	if config == nil {
		return rawConn, nil
	}

	conn := tls.Client(rawConn, config)
	err = conn.Handshake()
	if err != nil {
		rawConn.Close()
		return nil, err
	}
	return conn, nil
}

// DialContext connects to the provided address on the provided network.
func (d *Dialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.proxyDial(ctx, network, d.Proxy)
	if err != nil {
		return nil, err
	}

	hdr := d.ProxyHeader
	if hdr == nil {
		hdr = http.Header{}
	}
	if d.Userinfo != nil {
		hdr = hdr.Clone()
		hdr.Set(ProxyAuthorizationKey, basicAuth(d.Userinfo))
	}
	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: hdr,
	}

	// If there's no done channel (no deadline or cancellation
	// from the caller possible), at least set some (long)
	// timeout here. This will make sure we don't block forever
	// and leak a goroutine if the connection stops replying
	// after the TCP connect.
	connectCtx := ctx
	if ctx.Done() == nil {
		newCtx, cancel := context.WithTimeout(ctx, 1*time.Minute)
		defer cancel()
		connectCtx = newCtx
	}

	didReadResponse := make(chan struct{}) // closed after CONNECT write+read is done or fails
	var (
		resp *http.Response
	)
	// Write the CONNECT request & read the response.
	go func() {
		defer close(didReadResponse)
		err = connectReq.Write(conn)
		if err != nil {
			return
		}
		// Okay to use and discard buffered reader here, because
		// TLS server will not speak until spoken to.
		br := bufio.NewReader(conn)
		resp, err = http.ReadResponse(br, connectReq)
	}()
	select {
	case <-connectCtx.Done():
		conn.Close()
		<-didReadResponse
		return nil, connectCtx.Err()
	case <-didReadResponse:
		// resp or err now set
	}
	if err != nil {
		conn.Close()
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("failed proxying %d: %s", resp.StatusCode, resp.Status)
	}
	return conn, nil
}

// Dial connects to the provided address on the provided network.
func (d *Dialer) Dial(network string, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}
