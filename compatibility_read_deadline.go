package httpproxy

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

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
	return NewConnCompatibilityReadDeadline(c), nil
}

// autoListenerCompatibilityReadDeadline wraps only the accepted conns that may
// need it, the conns known to survive an interrupted read are returned as is.
type autoListenerCompatibilityReadDeadline struct {
	net.Listener
}

func (w autoListenerCompatibilityReadDeadline) Accept() (net.Conn, error) {
	c, err := w.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if !needCompatibilityReadDeadline(c) {
		return c, nil
	}
	return NewConnCompatibilityReadDeadline(c), nil
}

// needCompatibilityReadDeadline reports whether conn may not survive an
// interrupted read. A TLS conn forwards the deadline to the conn it is built
// on, so it is as safe as that conn.
func needCompatibilityReadDeadline(conn net.Conn) bool {
	for {
		switch c := conn.(type) {
		case *net.TCPConn, *net.UnixConn, *connCompatibilityReadDeadline:
			return false
		case *tls.Conn:
			conn = c.NetConn()
		default:
			return true
		}
	}
}

type connContextKey struct{}

// withConnTLS keeps the TLS conn hidden by the compatibility wrapper in ctx,
// it is used as http.Server.ConnContext.
func withConnTLS(ctx context.Context, conn net.Conn) context.Context {
	if c, ok := conn.(*connCompatibilityReadDeadline); ok {
		if tlsConn, ok := c.Conn.(*tls.Conn); ok {
			return context.WithValue(ctx, connContextKey{}, tlsConn)
		}
	}
	return ctx
}

// fillRequestTLS sets r.TLS as net/http does for a TLS conn, which it can
// not do by itself once the TLS conn is hidden by the compatibility wrapper.
func fillRequestTLS(r *http.Request) {
	if r.TLS != nil {
		return
	}
	if tlsConn, ok := r.Context().Value(connContextKey{}).(*tls.Conn); ok {
		state := tlsConn.ConnectionState()
		r.TLS = &state
	}
}

// NewConnCompatibilityReadDeadline this is a wrapper used to be compatible with
// the net.Conn after wrapping it so that it can be hijacked properly.
// there is no effect if the content is not manipulated.
//
// net/http interrupts its pending background read on Hijack by setting a read
// deadline in the past. A conn that keeps state across reads (a decoder, a
// cipher, ...) usually can not recover from a read interrupted that way.
// The returned conn never forwards such a deadline: it only makes the pending
// Read call return a timeout, while the read of the wrapped conn keeps going
// and its result is handed to the next Read call.
func NewConnCompatibilityReadDeadline(conn net.Conn) net.Conn {
	if conn == nil {
		return nil
	}
	if conn, ok := conn.(*connCompatibilityReadDeadline); ok {
		return conn
	}
	return &connCompatibilityReadDeadline{
		Conn:  conn,
		abort: make(chan struct{}),
	}
}

type connCompatibilityReadDeadline struct {
	net.Conn

	mu sync.Mutex
	// done is non-nil while a read of the wrapped conn is in flight or its
	// result has not been fully consumed yet, it is closed once the read returns.
	done chan struct{}
	buf  []byte
	data []byte
	err  error
	// abort is closed while the read deadline is in the past.
	abort   chan struct{}
	aborted bool
}

func (d *connCompatibilityReadDeadline) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		d.mu.Lock()
		if d.done == nil {
			if cap(d.buf) < len(p) {
				d.buf = make([]byte, len(p))
			}
			buf := d.buf[:len(p)]
			done := make(chan struct{})
			d.done = done
			go func() {
				n, err := d.Conn.Read(buf)
				d.mu.Lock()
				d.data, d.err = buf[:n], err
				d.mu.Unlock()
				close(done)
			}()
		}
		done, abort := d.done, d.abort
		d.mu.Unlock()

		select {
		case <-done:
		case <-abort:
			select {
			case <-done:
			default:
				return 0, os.ErrDeadlineExceeded
			}
		}

		d.mu.Lock()
		if d.done != done {
			// consumed by a concurrent Read
			d.mu.Unlock()
			continue
		}
		n := copy(p, d.data)
		d.data = d.data[n:]
		var err error
		if len(d.data) == 0 {
			err = d.err
			d.data, d.err, d.done = nil, nil, nil
		}
		d.mu.Unlock()
		return n, err
	}
}

func (d *connCompatibilityReadDeadline) SetReadDeadline(t time.Time) error {
	if d.setAborted(t) {
		return nil
	}
	return d.Conn.SetReadDeadline(t)
}

func (d *connCompatibilityReadDeadline) SetDeadline(t time.Time) error {
	if d.setAborted(t) {
		return d.Conn.SetWriteDeadline(t)
	}
	return d.Conn.SetDeadline(t)
}

// setAborted reports whether t is in the past, in which case pending and
// future Read calls return a timeout without interrupting the wrapped conn.
func (d *connCompatibilityReadDeadline) setAborted(t time.Time) bool {
	past := !t.IsZero() && !t.After(time.Now())
	d.mu.Lock()
	defer d.mu.Unlock()
	if past && !d.aborted {
		close(d.abort)
	} else if !past && d.aborted {
		d.abort = make(chan struct{})
	}
	d.aborted = past
	return past
}
