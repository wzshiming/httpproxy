package httpproxy

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
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
