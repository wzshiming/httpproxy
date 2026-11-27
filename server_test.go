package httpproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func proxyClient(proxy *TestServer) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return url.Parse(proxy.URL)
			},
		},
	}
}

// countListener counts the accepted connections.
type countListener struct {
	net.Listener
	n atomic.Int32
}

func (l *countListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

func TestForward(t *testing.T) {
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"Proxy-Authorization", "Proxy-Connection", "X-Hop"} {
			if r.Header.Get(name) != "" {
				t.Errorf("header %s is forwarded", name)
			}
		}
		w.Header().Set("X-Method", r.Method)
		w.Header().Set("X-Uri", r.RequestURI)
		switch r.URL.Path {
		case "/chunked":
			for i := 0; i != 3; i++ {
				fmt.Fprint(w, "chunk")
				w.(http.Flusher).Flush()
			}
		case "/nocontent":
			w.WriteHeader(http.StatusNoContent)
		case "/early":
			w.Header().Set("Link", "</style.css>; rel=preload")
			w.WriteHeader(http.StatusEarlyHints)
			fmt.Fprint(w, "early")
		default:
			body, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, "echo:%s", body)
		}
	}))
	listener := &countListener{Listener: target.Listener}
	target.Listener = listener
	target.Start()
	defer target.Close()

	proxy := proxyServer(&Server{Authentication: BasicAuth("u", "p")}, nil)
	defer proxy.Close()
	purl, _ := url.Parse(proxy.URL)
	purl.User = url.UserPassword("u", "p")
	cli := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(purl),
		},
	}

	tests := []struct {
		method string
		path   string
		body   string
		expect bool
		status int
		want   string
	}{
		{method: http.MethodGet, path: "/get?a=b", status: 200, want: "echo:"},
		{method: http.MethodPost, path: "/post", body: "hello", status: 200, want: "echo:hello"},
		{method: http.MethodPost, path: "/big", body: strings.Repeat("x", 1<<20), status: 200, want: "echo:" + strings.Repeat("x", 1<<20)},
		{method: http.MethodPost, path: "/expect", body: "hello", expect: true, status: 200, want: "echo:hello"},
		{method: http.MethodHead, path: "/head", status: 200, want: ""},
		{method: http.MethodGet, path: "/chunked", status: 200, want: "chunkchunkchunk"},
		{method: http.MethodGet, path: "/nocontent", status: 204, want: ""},
		{method: http.MethodGet, path: "/early", status: 200, want: "early"},
		{method: http.MethodGet, path: "/again", status: 200, want: "echo:"},
	}
	for _, tt := range tests {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				// Hide the type so that the body is sent chunked when it is big.
				body = io.MultiReader(strings.NewReader(tt.body))
			}
			req, err := http.NewRequest(tt.method, target.URL+tt.path, body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Connection", "X-Hop")
			req.Header.Set("X-Hop", "1")
			if tt.expect {
				req.Header.Set("Expect", "100-continue")
			}
			resp, err := cli.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tt.status {
				t.Fatalf("got status %d, want %d", resp.StatusCode, tt.status)
			}
			if string(got) != tt.want {
				t.Fatalf("got body %.40q, want %.40q", got, tt.want)
			}
			if m := resp.Header.Get("X-Method"); m != tt.method {
				t.Fatalf("got method %q, want %q", m, tt.method)
			}
			if u := resp.Header.Get("X-Uri"); u != tt.path {
				t.Fatalf("got uri %q, want %q", u, tt.path)
			}
		})
	}

	// All the requests are sent on one connection to the proxy,
	// which are forwarded on one connection to the target.
	if n := listener.n.Load(); n != 1 {
		t.Fatalf("got %d connections to the target, want 1", n)
	}
}

func TestForwardStaleUpstream(t *testing.T) {
	target := httptest.NewUnstartedServer(http.HandlerFunc(echoHandler))
	listener := &countListener{Listener: target.Listener}
	target.Listener = listener
	target.Start()
	defer target.Close()

	proxy := proxyServer(&Server{}, nil)
	defer proxy.Close()
	cli := proxyClient(proxy)

	for i := 0; i != 2; i++ {
		resp, err := cli.Get(target.URL + "/stale")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatal(resp.Status)
		}
		// Close the idle connection from the proxy to the target.
		target.CloseClientConnections()
		time.Sleep(50 * time.Millisecond)
	}
	if n := listener.n.Load(); n != 2 {
		t.Fatalf("got %d connections to the target, want 2", n)
	}
}

func TestForwardBadGateway(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()

	proxy := proxyServer(&Server{}, nil)
	defer proxy.Close()

	resp, err := proxyClient(proxy).Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatal(resp.Status)
	}

	dialer, err := NewDialer(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dialer.Dial("tcp", address)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("got error %v, want 502", err)
	}
}

func TestRawRequests(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer target.Close()
	host := strings.TrimPrefix(target.URL, "http://")

	proxy := proxyServer(&Server{Authentication: BasicAuth("u", "p")}, nil)
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)

	// The connection is kept alive after each of the replies.
	steps := []struct {
		request string
		status  int
	}{
		{"GET / HTTP/1.1\r\nHost: proxy\r\n\r\n", http.StatusNotFound},
		{"GET http://" + host + "/ HTTP/1.1\r\nHost: " + host + "\r\n\r\n", http.StatusProxyAuthRequired},
		{"CONNECT " + host + " HTTP/1.1\r\nHost: " + host + "\r\n\r\n", http.StatusProxyAuthRequired},
		{"GET http://" + host + "/ HTTP/1.1\r\nHost: " + host + "\r\nProxy-Authorization: Basic dTpw\r\n\r\n", http.StatusOK},
		// The first request through the tunnel is sent together with the CONNECT request.
		{"CONNECT " + host + " HTTP/1.1\r\nHost: " + host + "\r\nProxy-Authorization: Basic dTpw\r\n\r\n" +
			"GET /tunnel HTTP/1.1\r\nHost: " + host + "\r\n\r\n", http.StatusOK},
	}
	for _, step := range steps {
		_, err = io.WriteString(conn, step.request)
		if err != nil {
			t.Fatal(err)
		}
		method, _, _ := strings.Cut(step.request, " ")
		resp, err := http.ReadResponse(br, &http.Request{Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != step.status {
			t.Fatalf("got status %d, want %d", resp.StatusCode, step.status)
		}
		if resp.StatusCode == http.StatusProxyAuthRequired && resp.Header.Get(ProxyAuthenticateKey) != `Basic realm="proxy"` {
			t.Fatalf("got %s %q", ProxyAuthenticateKey, resp.Header.Get(ProxyAuthenticateKey))
		}
		if method == http.MethodConnect && resp.StatusCode == http.StatusOK {
			// What follows is the data of the tunnel.
			break
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("got body %q through the tunnel", body)
	}
}

func TestHTTP10(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "chunk")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "chunk")
	}))
	defer target.Close()
	host := strings.TrimPrefix(target.URL, "http://")

	proxy := proxyServer(&Server{}, nil)
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = io.WriteString(conn, "GET http://"+host+"/ HTTP/1.0\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	// The reply is delimited by closing the connection.
	reply, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(reply), "HTTP/1.0 200 OK\r\n") || !strings.HasSuffix(string(reply), "\r\n\r\nchunkchunk") {
		t.Fatalf("got reply %q", reply)
	}
}

func TestMalformedRequest(t *testing.T) {
	proxy := proxyServer(&Server{}, nil)
	defer proxy.Close()

	tests := []struct {
		name    string
		request string
		status  int
	}{
		{"malformed", "\x05\x01\x00\r\n\r\n", http.StatusBadRequest},
		{"too large", "GET / HTTP/1.1\r\nX: " + strings.Repeat("x", maxHeaderBytes+8192) + "\r\n\r\n", http.StatusRequestHeaderFieldsTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			go io.WriteString(conn, tt.request)
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.status {
				t.Fatalf("got status %d, want %d", resp.StatusCode, tt.status)
			}
		})
	}
}

func TestHandshakeTimeout(t *testing.T) {
	proxy := proxyServer(&Server{HandshakeTimeout: 100 * time.Millisecond}, nil)
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	// The server closes the connection of the client that stalls.
	_, err = conn.Read(make([]byte, 1))
	if err != io.EOF {
		t.Fatalf("got error %v, want EOF", err)
	}
}

func TestForwardHTTPS(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(echoHandler))
	defer target.Close()

	proxy := proxyServer(&Server{TLSClientConfig: target.Client().Transport.(*http.Transport).TLSClientConfig}, nil)
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	for i := 0; i != 2; i++ {
		_, err = io.WriteString(conn, "GET "+target.URL+"/https HTTP/1.1\r\nHost: target\r\n\r\n")
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "/https") {
			t.Fatalf("got %s %q", resp.Status, body)
		}
	}
}

func TestUpgrade(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" || r.Header.Get("Connection") != "Upgrade" {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		// The first data is sent together with the reply.
		rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\nhello ")
		rw.Flush()
		io.Copy(conn, rw)
	}))
	defer target.Close()
	host := strings.TrimPrefix(target.URL, "http://")

	proxy := proxyServer(&Server{}, nil)
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = io.WriteString(conn, "GET http://"+host+"/ HTTP/1.1\r\nHost: "+host+"\r\nConnection: keep-alive, Upgrade\r\nUpgrade: echo\r\n\r\nworld")
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != "echo" || resp.Header.Get("Connection") != "Upgrade" {
		t.Fatalf("got %s %v", resp.Status, resp.Header)
	}
	// The target replies what is left after the client is done with sending.
	conn.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestConnectHalfClose(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Reply only after the client is done with sending.
		data, _ := io.ReadAll(conn)
		time.Sleep(50 * time.Millisecond)
		conn.Write(append([]byte("echo:"), data...))
	}()

	proxy := proxyServer(&Server{}, nil)
	defer proxy.Close()
	dialer, err := NewDialer(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.Dial("tcp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	io.WriteString(conn, "hello")
	conn.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "echo:hello" {
		t.Fatalf("got %q", got)
	}
}

// TestEarlyResponse checks that the reply of the target is forwarded
// even if it does not wait for the body of the request.
func TestEarlyResponse(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 2\r\n\r\nno")
				// Wait for the proxy to close the connection.
				io.Copy(io.Discard, conn)
			}()
		}
	}()
	host := target.Addr().String()

	proxy := proxyServer(&Server{}, nil)
	defer proxy.Close()

	for _, expect := range []string{"", "Expect: 100-continue\r\n"} {
		conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		// The body is never sent.
		_, err = io.WriteString(conn, "POST http://"+host+"/ HTTP/1.1\r\nHost: "+host+"\r\nContent-Length: 5\r\n"+expect+"\r\n")
		if err != nil {
			t.Fatal(err)
		}
		reply, err := io.ReadAll(conn)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(reply), "HTTP/1.1 403 Forbidden\r\n") ||
			!strings.Contains(string(reply), "Connection: close\r\n") ||
			!strings.HasSuffix(string(reply), "\r\n\r\nno") {
			t.Fatalf("got reply %q", reply)
		}
	}
}

func TestIdleTimeout(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer target.Close()
	host := strings.TrimPrefix(target.URL, "http://")

	proxy := proxyServer(&Server{IdleTimeout: 100 * time.Millisecond}, nil)
	defer proxy.Close()

	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = io.WriteString(conn, "GET http://"+host+"/ HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Close {
		t.Fatalf("got %s close=%v", resp.Status, resp.Close)
	}
	// The server closes the connection that is idle.
	_, err = br.ReadByte()
	if err != io.EOF {
		t.Fatalf("got error %v, want EOF", err)
	}
}

func TestViaAndForwardedFor(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Got-Via", r.Header.Get("Via"))
		w.Header().Set("X-Got-Forwarded-For", r.Header.Get("X-Forwarded-For"))
		w.Header().Set("Via", "1.1 origin")
	}))
	defer target.Close()

	tests := []struct {
		name         string
		server       *Server
		via          string
		forwardedFor string
		respVia      string
	}{
		{"default", &Server{}, "1.0 client", "10.0.0.1", "1.1 origin"},
		{"enabled", &Server{Via: "proxy", ForwardedFor: true}, "1.0 client, 1.1 proxy", "10.0.0.1, 127.0.0.1", "1.1 origin, 1.1 proxy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := proxyServer(tt.server, nil)
			defer proxy.Close()

			req, err := http.NewRequest(http.MethodGet, target.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Via", "1.0 client")
			req.Header.Set("X-Forwarded-For", "10.0.0.1")
			resp, err := proxyClient(proxy).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if got := resp.Header.Get("X-Got-Via"); got != tt.via {
				t.Errorf("got request Via %q, want %q", got, tt.via)
			}
			if got := resp.Header.Get("X-Got-Forwarded-For"); got != tt.forwardedFor {
				t.Errorf("got X-Forwarded-For %q, want %q", got, tt.forwardedFor)
			}
			if got := resp.Header.Get("Via"); got != tt.respVia {
				t.Errorf("got response Via %q, want %q", got, tt.respVia)
			}
		})
	}
}

func TestSimpleServerHTTPS(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(echoHandler))
	defer target.Close()

	s, err := NewSimpleServer("https://u:p@127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Start(context.Background())
	if err == nil {
		s.Close()
		t.Fatal("started without TLSConfig")
	}

	// HTTP/1.1 is negotiated even if the config offers HTTP/2.
	s.TLSConfig = tlsConfig2
	err = s.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !strings.HasPrefix(s.ProxyURL(), "https://u:p@127.0.0.1:") {
		t.Fatalf("got proxy url %q", s.ProxyURL())
	}

	purl, _ := url.Parse(s.ProxyURL())
	dialer, err := NewDialer(s.ProxyURL())
	if err != nil {
		t.Fatal(err)
	}
	dialer.TLSClientConfig = tlsConfig2
	clients := map[string]*http.Transport{
		"proxy":   {Proxy: http.ProxyURL(purl), TLSClientConfig: tlsConfig2},
		"connect": {DialContext: dialer.DialContext},
	}
	for name, transport := range clients {
		t.Run(name, func(t *testing.T) {
			resp, err := (&http.Client{Transport: transport}).Get(target.URL + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "/"+name) {
				t.Fatalf("got %s %q", resp.Status, body)
			}
		})
	}
}

// TestUnsupportedALPN checks that a connection on which a protocol
// other than HTTP/1.1 is negotiated is closed.
func TestUnsupportedALPN(t *testing.T) {
	proxy := proxyServer(&Server{}, tlsConfig2.Clone())
	defer proxy.Close()

	conn, err := tls.Dial("tcp", proxy.Listener.Addr().String(), tlsConfig2)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if proto := conn.ConnectionState().NegotiatedProtocol; proto != "h2" {
		t.Fatalf("negotiated %q", proto)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = conn.Read(make([]byte, 1))
	if err != io.EOF {
		t.Fatalf("got error %v, want EOF", err)
	}
}
