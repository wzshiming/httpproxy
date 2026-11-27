package httpproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// maxHeaderBytes is the maximum size of a request header the server will read.
const maxHeaderBytes = 1 << 20

var errHeaderTooLarge = errors.New("request header too large")

// Server is accepting connections and handling the details of the HTTP proxy protocol
type Server struct {
	// Authentication is proxy authentication
	Authentication Authentication
	// ProxyDial specifies the optional proxyDial function for
	// establishing the transport connection.
	ProxyDial func(ctx context.Context, network string, address string) (net.Conn, error)
	// ProxyListen specifies the optional proxyListen function for
	// establishing the transport connection.
	ProxyListen func(ctx context.Context, network string, address string) (net.Listener, error)
	// HandshakeTimeout is the maximum amount of time to read the header of
	// the first request of a connection, protecting the server from clients
	// that connect and then stall. Zero means no timeout.
	HandshakeTimeout time.Duration
	// IdleTimeout is the maximum amount of time to wait for the next
	// request of a connection that is kept alive. Zero means no timeout.
	IdleTimeout time.Duration
	// TLSClientConfig specifies the optional TLS configuration to use for
	// forwarding the requests of which the scheme of the URL is https.
	// The requests through CONNECT are not affected.
	TLSClientConfig *tls.Config
	// Via is the optional name of the proxy, if it is not empty the proxy
	// adds itself to the Via header of the forwarded requests and responses.
	// The requests through CONNECT are not affected.
	Via string
	// ForwardedFor specifies whether the address of the client is added to the
	// X-Forwarded-For header of the forwarded requests.
	// The requests through CONNECT are not affected.
	ForwardedFor bool
	// Logger error log
	Logger Logger
	// Context is default context
	Context context.Context
	// BytesPool getting and returning temporary bytes for use by io.CopyBuffer
	BytesPool BytesPool
}

type Logger interface {
	Println(v ...interface{})
}

// NewServer creates a new Server
func NewServer() *Server {
	return &Server{
		HandshakeTimeout: 10 * time.Second,
	}
}

// ListenAndServe is used to create a listener and serve on it
func (s *Server) ListenAndServe(network, addr string) error {
	l, err := s.proxyListen(s.context(), network, addr)
	if err != nil {
		return err
	}
	return s.Serve(l)
}

// ListenAndServeTLS is used to create a listener and serve on it with TLS
func (s *Server) ListenAndServeTLS(network, addr string, config *tls.Config) error {
	l, err := s.proxyListen(s.context(), network, addr)
	if err != nil {
		return err
	}
	return s.ServeTLS(l, config)
}

// ServeTLS is used to serve connections from a listener with TLS.
// Only HTTP/1.1 is negotiated, whatever NextProtos of the config is.
func (s *Server) ServeTLS(l net.Listener, config *tls.Config) error {
	if config == nil {
		return errors.New("TLS config is required")
	}
	config = config.Clone()
	config.NextProtos = []string{"http/1.1"}
	return s.Serve(tls.NewListener(l, config))
}

// Serve is used to serve connections from a listener
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go s.ServeConn(conn)
	}
}

// ServeConn is used to serve a single connection.
func (s *Server) ServeConn(conn net.Conn) {
	defer conn.Close()
	err := s.serveConn(conn)
	if err != nil && s.Logger != nil && !isClosedConnError(err) {
		s.Logger.Println(err)
	}
}

func (s *Server) serveConn(conn net.Conn) error {
	limit := &limitReader{r: conn}
	c := &serverConn{
		server: s,
		conn:   conn,
		br:     bufio.NewReader(limit),
	}
	defer c.closeUpstream()

	if tlsConn, ok := conn.(*tls.Conn); ok {
		if s.HandshakeTimeout > 0 {
			conn.SetDeadline(time.Now().Add(s.HandshakeTimeout))
		}
		err := tlsConn.Handshake()
		if err != nil {
			return fmt.Errorf("handshake with %s: %w", conn.RemoteAddr(), err)
		}
		if s.HandshakeTimeout > 0 {
			conn.SetDeadline(time.Time{})
		}
		// Only HTTP/1.x is spoken by the server.
		if proto := tlsConn.ConnectionState().NegotiatedProtocol; proto != "" && proto != "http/1.1" {
			return fmt.Errorf("unsupported protocol %q negotiated with %s, the TLS config must not offer it", proto, conn.RemoteAddr())
		}
	}

	for first := true; ; first = false {
		timeout := s.IdleTimeout
		if first {
			timeout = s.HandshakeTimeout
		}
		if timeout > 0 {
			conn.SetReadDeadline(time.Now().Add(timeout))
		}
		limit.n = maxHeaderBytes
		req, err := http.ReadRequest(c.br)
		if err != nil {
			if !first && isTimeout(err) {
				// The idle connection is closed quietly.
				return nil
			}
			if limit.n <= 0 {
				c.writeError(http.StatusRequestHeaderFieldsTooLarge, errHeaderTooLarge.Error(), true)
				return errHeaderTooLarge
			}
			if err == io.EOF || isClosedConnError(err) {
				// The client is gone or done with the connection.
				return nil
			}
			if !isNetError(err) {
				c.writeError(http.StatusBadRequest, "malformed HTTP request", true)
			}
			return fmt.Errorf("read request from %s: %w", conn.RemoteAddr(), err)
		}
		limit.n = math.MaxInt64
		if timeout > 0 {
			conn.SetReadDeadline(time.Time{})
		}

		keepAlive, err := c.handle(req)
		if err != nil || !keepAlive {
			return err
		}
	}
}

// serverConn is the state of a client connection.
type serverConn struct {
	server *Server
	conn   net.Conn
	br     *bufio.Reader

	// upstream is the connection of the last forwarded request,
	// it is reused if the next request has the same destination.
	upstream    net.Conn
	upstreamBr  *bufio.Reader
	upstreamKey string
}

// handle handles a request, keepAlive reports whether the
// client connection can be used to read the next request.
func (c *serverConn) handle(req *http.Request) (keepAlive bool, err error) {
	s := c.server
	isProxy := req.Method == http.MethodConnect || req.URL.Host != ""
	if !isProxy {
		keepAlive = canKeepAlive(req)
		return keepAlive, c.writeError(http.StatusNotFound, "", !keepAlive)
	}

	if s.Authentication != nil {
		username, password, ok := parseBasicAuth(req.Header.Get(ProxyAuthorizationKey))
		if !ok || !s.Authentication.Auth(username, password) {
			keepAlive = canKeepAlive(req)
			return keepAlive, c.writeError(http.StatusProxyAuthRequired, "", !keepAlive)
		}
	}

	if req.Method == http.MethodConnect {
		return false, c.handleConnect(req)
	}
	return c.handleForward(req)
}

func (c *serverConn) handleConnect(req *http.Request) error {
	s := c.server
	address := req.URL.Host
	if address == "" {
		address = req.Host
	}
	target, err := s.proxyDial(s.context(), "tcp", address)
	if err != nil {
		e := fmt.Sprintf("connect to %q failed: %v", address, err)
		if err := c.writeError(gatewayStatus(err), e, true); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		return errors.New(e)
	}
	defer target.Close()

	_, err = io.WriteString(c.conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	if err != nil {
		return fmt.Errorf("failed to send reply: %v", err)
	}
	return c.tunnel(target)
}

// tunnel relays the data between the client and the target until both directions are done.
func (c *serverConn) tunnel(target net.Conn) error {
	s := c.server
	var buf1, buf2 []byte
	if s.BytesPool != nil {
		buf1 = s.BytesPool.Get()
		buf2 = s.BytesPool.Get()
		defer func() {
			s.BytesPool.Put(buf1)
			s.BytesPool.Put(buf2)
		}()
	} else {
		buf1 = make([]byte, 32*1024)
		buf2 = make([]byte, 32*1024)
	}
	return tunnel(s.context(), target, newBufConn(c.conn, c.br), buf1, buf2)
}

func (c *serverConn) handleForward(req *http.Request) (keepAlive bool, err error) {
	var port string
	switch req.URL.Scheme {
	case "http":
		port = "80"
	case "https":
		port = "443"
	default:
		e := fmt.Sprintf("unsupported protocol scheme %q", req.URL.Scheme)
		return false, c.writeError(http.StatusBadRequest, e, true)
	}
	address := req.URL.Host
	if req.URL.Port() == "" {
		address = net.JoinHostPort(req.URL.Hostname(), port)
	}

	keepAlive = !req.Close && req.ProtoAtLeast(1, 1) &&
		!strings.EqualFold(req.Header.Get("Proxy-Connection"), "close")
	expectContinue := strings.EqualFold(req.Header.Get("Expect"), "100-continue")
	upgrade := upgradeType(req.Header)

	// Turn the request read from the client into the one sent to the target.
	req.RequestURI = ""
	req.Close = false
	removeHopByHopHeaders(req.Header)
	c.addVia(req.Header, req.ProtoMajor, req.ProtoMinor)
	if c.server.ForwardedFor {
		if host, _, err := net.SplitHostPort(c.conn.RemoteAddr().String()); err == nil {
			appendHeader(req.Header, "X-Forwarded-For", host)
		}
	}
	if upgrade != "" {
		// Switching protocols is negotiated end to end through the proxy.
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", upgrade)
	}
	if _, ok := req.Header["User-Agent"]; !ok {
		// Prevent Request.Write from adding the default User-Agent.
		req.Header.Set("User-Agent", "")
	}
	var body *bodyReader
	if req.Body != http.NoBody {
		body = &bodyReader{ReadCloser: req.Body}
		req.Body = body
	}

	resp, written, err := c.roundTrip(req, address, expectContinue)
	if err != nil {
		c.closeUpstream()
		e := fmt.Sprintf("forward to %q failed: %v", address, err)
		if err := c.writeError(gatewayStatus(err), e, true); err != nil {
			return false, fmt.Errorf("failed to send reply: %v", err)
		}
		return false, errors.New(e)
	}

	if resp.StatusCode == http.StatusSwitchingProtocols {
		return false, c.handleUpgrade(resp, upgrade)
	}

	// The target may reply before the whole body of the request is sent to
	// it, the client connection can not be used for the next request then.
	bodyDone := body == nil || body.done.Load()
	if !bodyDone {
		keepAlive = false
	}

	upstreamClose := resp.Close
	removeHopByHopHeaders(resp.Header)
	c.addVia(resp.Header, resp.ProtoMajor, resp.ProtoMinor)
	resp.ProtoMajor, resp.ProtoMinor = req.ProtoMajor, req.ProtoMinor
	if !req.ProtoAtLeast(1, 1) {
		// HTTP/1.0 clients do not understand chunked encoding,
		// the body is delimited by closing the connection instead.
		resp.TransferEncoding = nil
	}
	if resp.ContentLength < 0 && len(resp.TransferEncoding) == 0 && bodyAllowed(req, resp) {
		keepAlive = false
	}
	resp.Close = !keepAlive

	err = resp.Write(c.conn)
	resp.Body.Close()
	if err != nil {
		c.closeUpstream()
		return false, fmt.Errorf("forward to %q failed: %w", address, err)
	}
	if !bodyDone {
		c.closeUpstream()
		return false, nil
	}
	if upstreamClose {
		c.closeUpstream()
	}
	if written != nil {
		// The body is read, what is left of the request is being sent.
		select {
		case err = <-written:
		case <-time.After(time.Second):
			c.closeUpstream()
			err = <-written
		}
		if err != nil {
			c.closeUpstream()
		}
	}
	return keepAlive, nil
}

// handleUpgrade relays the data of the protocol the target switched to.
func (c *serverConn) handleUpgrade(resp *http.Response, upgrade string) error {
	if upgrade == "" {
		c.closeUpstream()
		e := "unexpected switching protocols"
		if err := c.writeError(http.StatusBadGateway, e, true); err != nil {
			return fmt.Errorf("failed to send reply: %v", err)
		}
		return errors.New(e)
	}
	if u := upgradeType(resp.Header); u != "" {
		upgrade = u
	}
	removeHopByHopHeaders(resp.Header)
	c.addVia(resp.Header, resp.ProtoMajor, resp.ProtoMinor)
	resp.Header.Set("Connection", "Upgrade")
	resp.Header.Set("Upgrade", upgrade)

	// The tunnel takes over the connection to the target.
	target := newBufConn(c.upstream, c.upstreamBr)
	c.upstream = nil
	c.closeUpstream()
	defer target.Close()

	err := writeHeader(c.conn, resp.StatusCode, resp.Header)
	if err != nil {
		return fmt.Errorf("failed to send reply: %v", err)
	}
	return c.tunnel(target)
}

// roundTrip sends the request to the address and returns the response,
// the body of which is read from the upstream connection.
// If the request has a body, it is sent in the background,
// and the result is sent to the returned channel.
func (c *serverConn) roundTrip(req *http.Request, address string, expectContinue bool) (*http.Response, <-chan error, error) {
	key := req.URL.Scheme + "://" + address
	reused := c.upstream != nil && c.upstreamKey == key
	for {
		if !reused {
			c.closeUpstream()
			upstream, err := c.dialUpstream(req.URL.Scheme, address)
			if err != nil {
				return nil, nil, err
			}
			c.upstream = upstream
			c.upstreamBr = bufio.NewReader(upstream)
			c.upstreamKey = key
		}

		resp, written, err := c.exchange(req, expectContinue)
		if err == nil {
			return resp, written, nil
		}
		// A reused connection may have been closed by the target while it
		// was idle, try again with a new one if the request can be replayed.
		if !reused || req.Body != http.NoBody {
			return nil, nil, err
		}
		reused = false
	}
}

func (c *serverConn) dialUpstream(scheme, address string) (net.Conn, error) {
	s := c.server
	ctx := s.context()
	conn, err := s.proxyDial(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if scheme != "https" {
		return conn, nil
	}

	config := s.TLSClientConfig
	if config == nil {
		config = &tls.Config{}
	} else {
		config = config.Clone()
	}
	if config.ServerName == "" {
		config.ServerName, _, _ = net.SplitHostPort(address)
	}
	tlsConn := tls.Client(conn, config)
	err = tlsConn.HandshakeContext(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

func (c *serverConn) exchange(req *http.Request, expectContinue bool) (*http.Response, <-chan error, error) {
	var written chan error
	if req.Body == http.NoBody {
		err := req.Write(c.upstream)
		if err != nil {
			return nil, nil, err
		}
	} else {
		// The response is read while the body is being sent, the target may
		// reply early, or wait for the body until it replied 100 Continue.
		written = make(chan error, 1)
		upstream := c.upstream
		go func() {
			written <- req.Write(upstream)
		}()
	}
	for {
		resp, err := http.ReadResponse(c.upstreamBr, req)
		if err != nil {
			return nil, nil, err
		}
		if resp.StatusCode >= 200 || resp.StatusCode == http.StatusSwitchingProtocols {
			return resp, written, nil
		}
		// Informational responses precede the final response.
		if !req.ProtoAtLeast(1, 1) || resp.StatusCode == http.StatusContinue && !expectContinue {
			// HTTP/1.0 clients do not understand them.
			continue
		}
		err = writeHeader(c.conn, resp.StatusCode, resp.Header)
		if err != nil {
			return nil, nil, err
		}
	}
}

// addVia adds the proxy to the Via header of a message received in the protocol version.
func (c *serverConn) addVia(h http.Header, major, minor int) {
	if via := c.server.Via; via != "" {
		appendHeader(h, "Via", fmt.Sprintf("%d.%d %s", major, minor, via))
	}
}

// appendHeader appends the value to the list of the header.
func appendHeader(h http.Header, key, value string) {
	if prior := h.Values(key); len(prior) != 0 {
		value = strings.Join(prior, ", ") + ", " + value
	}
	h.Set(key, value)
}

func (c *serverConn) closeUpstream() {
	if c.upstream != nil {
		c.upstream.Close()
	}
	c.upstream = nil
	c.upstreamBr = nil
	c.upstreamKey = ""
}

// writeError replies to the request with the code and the message.
func (c *serverConn) writeError(code int, message string, closeConn bool) error {
	if message == "" {
		message = http.StatusText(code)
	}
	message += "\n"
	header := http.Header{
		"Content-Type":   {"text/plain; charset=utf-8"},
		"Content-Length": {strconv.Itoa(len(message))},
	}
	if code == http.StatusProxyAuthRequired {
		header.Set(ProxyAuthenticateKey, BasicAuthName+` realm="proxy"`)
	}
	if closeConn {
		header.Set("Connection", "close")
	}
	err := writeHeader(c.conn, code, header)
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.conn, message)
	return err
}

func writeHeader(w io.Writer, code int, header http.Header) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "HTTP/1.1 %d %s\r\n", code, http.StatusText(code))
	header.Write(bw)
	bw.WriteString("\r\n")
	return bw.Flush()
}

// canKeepAlive reports whether the client connection can be used to
// read the next request after replying without reading the body.
func canKeepAlive(req *http.Request) bool {
	return !req.Close && req.ProtoAtLeast(1, 1) && req.Body == http.NoBody
}

// gatewayStatus returns the status code to reply for the error of reaching the target.
func gatewayStatus(err error) int {
	if isTimeout(err) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// upgradeType returns the protocol to switch to if the header asks for it.
func upgradeType(h http.Header) string {
	for _, value := range h.Values("Connection") {
		for name := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(textproto.TrimString(name), "Upgrade") {
				return h.Get("Upgrade")
			}
		}
	}
	return ""
}

// bodyReader records whether the body is read completely.
type bodyReader struct {
	io.ReadCloser
	done atomic.Bool
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.done.Store(true)
	}
	return n, err
}

// bodyAllowed reports whether the response to the request has a body.
func bodyAllowed(req *http.Request, resp *http.Response) bool {
	if req.Method == http.MethodHead {
		return false
	}
	code := resp.StatusCode
	return code >= 200 && code != http.StatusNoContent && code != http.StatusNotModified
}

// hopByHopHeaders are consumed by the proxy and must not be forwarded.
// See RFC 7230, section 6.1.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func removeHopByHopHeaders(h http.Header) {
	for _, value := range h.Values("Connection") {
		for name := range strings.SplitSeq(value, ",") {
			if name = textproto.TrimString(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

func (s *Server) proxyDial(ctx context.Context, network, address string) (net.Conn, error) {
	proxyDial := s.ProxyDial
	if proxyDial == nil {
		var dialer net.Dialer
		proxyDial = dialer.DialContext
	}
	return proxyDial(ctx, network, address)
}

func (s *Server) proxyListen(ctx context.Context, network, address string) (net.Listener, error) {
	proxyListen := s.ProxyListen
	if proxyListen == nil {
		var listenConfig net.ListenConfig
		proxyListen = listenConfig.Listen
	}
	return proxyListen(ctx, network, address)
}

func (s *Server) context() context.Context {
	if s.Context == nil {
		return context.Background()
	}
	return s.Context
}

// limitReader limits the bytes read from r, unlike io.LimitedReader
// it reports errHeaderTooLarge when the limit is reached.
type limitReader struct {
	r io.Reader
	n int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errHeaderTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

func newBufConn(conn net.Conn, br *bufio.Reader) net.Conn {
	if br.Buffered() == 0 {
		// If there's no buffered data to be read,
		// we can just discard the bufio.Reader.
		return conn
	}
	return &bufConn{conn, br}
}

// bufConn wraps a net.Conn, but reads drain the bufio.Reader first.
type bufConn struct {
	net.Conn
	*bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) {
	if c.Reader == nil {
		return c.Conn.Read(p)
	}
	n := c.Reader.Buffered()
	if n == 0 {
		c.Reader = nil
		return c.Conn.Read(p)
	}
	if n < len(p) {
		p = p[:n]
	}
	return c.Reader.Read(p)
}

// CloseWrite shuts down the writing side of the connection if it is supported.
func (c *bufConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return errors.ErrUnsupported
}

func isNetError(err error) bool {
	var ne net.Error
	return errors.As(err, &ne)
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// isClosedConnError reports whether err is an error from use of a closed
// network connection.
func isClosedConnError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe) ||
		strings.Contains(err.Error(), "use of closed network connection")
}
