package httpproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewListenerCompatibilityReadDeadline(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))

	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	listener = newHexListener(listener)
	listener = NewListenerCompatibilityReadDeadline(listener)

	s, err := NewSimpleServer("http://u:p@:0")
	if err != nil {
		t.Fatal(err)
	}

	s.Listener = listener
	s.Start(context.Background())
	defer s.Close()

	dial, err := NewDialer(s.ProxyURL())
	if err != nil {
		t.Fatal(err)
	}
	dial.ProxyDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := net.Dial(network, address)
		if err != nil {
			return nil, err
		}
		conn = newHexConn(conn)
		return conn, nil
	}
	cli := testServer.Client()
	cli.Transport = &http.Transport{
		DialContext: dial.DialContext,
	}

	resp, err := cli.Get(testServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// The client stays idle after CONNECT, so the read interrupted by Hijack
// can not be completed by incoming data.
// SimpleServer applies the compatibility by itself.
func TestSimpleServerCompatibilityReadDeadline(t *testing.T) {
	t.Run("stateful", func(t *testing.T) {
		testSimpleServerCompatibilityReadDeadline(t, newHexListener, newHexConn, false)
	})
	// TLS on top of a stateful conn, the request must still carry the TLS state.
	t.Run("tls over stateful", func(t *testing.T) {
		testSimpleServerCompatibilityReadDeadline(t,
			func(listener net.Listener) net.Listener {
				return tls.NewListener(newHexListener(listener), tlsConfig1)
			},
			func(conn net.Conn) net.Conn {
				return tls.Client(newHexConn(conn), tlsConfig1)
			},
			true,
		)
	})
	// Plain TLS is not wrapped and keeps being handled by net/http.
	t.Run("tls", func(t *testing.T) {
		testSimpleServerCompatibilityReadDeadline(t,
			func(listener net.Listener) net.Listener {
				return tls.NewListener(listener, tlsConfig1)
			},
			func(conn net.Conn) net.Conn {
				return tls.Client(conn, tlsConfig1)
			},
			true,
		)
	})
}

func testSimpleServerCompatibilityReadDeadline(t *testing.T, wrapListener func(net.Listener) net.Listener, wrapConn func(net.Conn) net.Conn, wantTLS bool) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer testServer.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	s, err := NewSimpleServer("http://127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	gotTLS := make(chan bool, 1)
	s.Authentication = AuthenticationFunc(func(w http.ResponseWriter, r *http.Request) bool {
		gotTLS <- r.TLS != nil && r.TLS.HandshakeComplete
		return true
	})
	connContext := false
	s.Server.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		connContext = true
		return ctx
	}
	s.Listener = wrapListener(listener)
	s.Start(context.Background())
	defer s.Close()

	dial, err := NewDialer(s.ProxyURL())
	if err != nil {
		t.Fatal(err)
	}
	dial.ProxyDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := net.Dial(network, address)
		if err != nil {
			return nil, err
		}
		return wrapConn(conn), nil
	}

	conn, err := dial.DialContext(context.Background(), "tcp", testServer.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if got := <-gotTLS; got != wantTLS {
		t.Errorf("request with TLS state = %v, want %v", got, wantTLS)
	}
	if !connContext {
		t.Error("ConnContext of the server is not called")
	}

	time.Sleep(1500 * time.Millisecond)

	_, err = fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
}

func newHexListener(listener net.Listener) net.Listener {
	return hexListener{
		Listener: listener,
	}
}

type hexListener struct {
	net.Listener
}

func (h hexListener) Accept() (net.Conn, error) {
	conn, err := h.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newHexConn(conn), nil
}

func newHexConn(conn net.Conn) net.Conn {
	return hexConn{
		Conn: conn,
		r:    hex.NewDecoder(conn),
		w:    hex.NewEncoder(conn),
	}
}

type hexConn struct {
	net.Conn
	r io.Reader
	w io.Writer
}

func (h hexConn) Read(p []byte) (n int, err error) {
	return h.r.Read(p)
}

func (h hexConn) Write(p []byte) (n int, err error) {
	return h.w.Write(p)
}

func TestAutoListenerCompatibilityReadDeadline(t *testing.T) {
	tests := []struct {
		name    string
		wrap    func(net.Listener) net.Listener
		wrapped bool
	}{
		{"tcp", func(l net.Listener) net.Listener { return l }, false},
		{"stateful", newHexListener, true},
		{"tls", func(l net.Listener) net.Listener { return tls.NewListener(l, tlsConfig1) }, false},
		{"tls over stateful", func(l net.Listener) net.Listener {
			return tls.NewListener(newHexListener(l), tlsConfig1)
		}, true},
		{"explicit", NewListenerCompatibilityReadDeadline, true},
		{"tls over explicit", func(l net.Listener) net.Listener {
			return tls.NewListener(NewListenerCompatibilityReadDeadline(newHexListener(l)), tlsConfig1)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			inner := tt.wrap(listener)

			cli, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			conn, err := autoListenerCompatibilityReadDeadline{inner}.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			_, wrapped := conn.(*connCompatibilityReadDeadline)
			if wrapped != tt.wrapped {
				t.Errorf("wrapped = %v, want %v for %T", wrapped, tt.wrapped, conn)
			}
		})
	}
}
