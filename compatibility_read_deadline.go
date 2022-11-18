package httpproxy

import (
	"net"
	"time"
)

// aLongTimeAgo is a non-zero time, far in the past, used for
// immediate cancellation of network operations.
// copies from http
var aLongTimeAgo = time.Unix(1, 0)

// NewListenerCompatibilityReadDeadline this is a wrapper used to be compatible with
// the contents of ServerConn after wrapping it so that it can be hijacked properly.
// there is no effect if the content is not manipulated.
func NewListenerCompatibilityReadDeadline(listener net.Listener) net.Listener {
	return listenerCompatibilityReadDeadline{listener}
}

type listenerCompatibilityReadDeadline struct {
	net.Listener
}

func (w listenerCompatibilityReadDeadline) Accept() (net.Conn, error) {
	c, err := w.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return connCompatibilityReadDeadline{c}, nil
}

type connCompatibilityReadDeadline struct {
	net.Conn
}

func (d connCompatibilityReadDeadline) SetReadDeadline(t time.Time) error {
	if aLongTimeAgo == t {
		t = time.Now().Add(time.Second)
	}
	return d.Conn.SetReadDeadline(t)
}
