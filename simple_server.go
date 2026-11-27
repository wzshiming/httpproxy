package httpproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"time"
)

// SimpleServer is a simplified server, which can be configured as easily as client.
type SimpleServer struct {
	Server
	Listener net.Listener
	// Scheme is the scheme of the proxy, http or https.
	Scheme string
	// TLSConfig is the TLS configuration to serve with,
	// it is required if the scheme is https.
	TLSConfig *tls.Config
	Network   string
	Address   string
	Username  string
	Password  string
}

// NewSimpleServer creates a new SimpleServer
func NewSimpleServer(addr string) (*SimpleServer, error) {
	s := &SimpleServer{}
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	defaultPort := "80"
	switch u.Scheme {
	case "http":
	case "https":
		defaultPort = "443"
	default:
		return nil, fmt.Errorf("unsupported protocol '%s'", u.Scheme)
	}
	s.Scheme = u.Scheme
	host := u.Host
	port := u.Port()
	if port == "" {
		port = defaultPort
		hostname := u.Hostname()
		host = net.JoinHostPort(hostname, port)
	}
	if u.User != nil {
		s.Username = u.User.Username()
		s.Password, _ = u.User.Password()
		s.Authentication = BasicAuth(s.Username, s.Password)
	}
	s.HandshakeTimeout = 10 * time.Second
	s.Address = host
	s.Network = "tcp"
	return s, nil
}

// Run the server
func (s *SimpleServer) Run(ctx context.Context) error {
	err := s.listen(ctx)
	if err != nil {
		return err
	}
	return s.serve()
}

// Start the server
func (s *SimpleServer) Start(ctx context.Context) error {
	err := s.listen(ctx)
	if err != nil {
		return err
	}
	go s.serve()
	return nil
}

func (s *SimpleServer) listen(ctx context.Context) error {
	if s.Scheme == "https" && s.TLSConfig == nil {
		return fmt.Errorf("TLSConfig is required for protocol 'https'")
	}
	if s.Listener == nil {
		var listenConfig net.ListenConfig
		listener, err := listenConfig.Listen(ctx, s.Network, s.Address)
		if err != nil {
			return err
		}
		s.Listener = listener
	}
	s.Address = s.Listener.Addr().String()
	return nil
}

func (s *SimpleServer) serve() error {
	if s.Scheme == "https" {
		return s.ServeTLS(s.Listener, s.TLSConfig)
	}
	return s.Serve(s.Listener)
}

// Close closes the listener
func (s *SimpleServer) Close() error {
	if s.Listener == nil {
		return nil
	}
	return s.Listener.Close()
}

// ProxyURL returns the URL of the proxy
func (s *SimpleServer) ProxyURL() string {
	u := url.URL{
		Scheme: s.Scheme,
		Host:   s.Address,
	}
	if s.Username != "" {
		u.User = url.UserPassword(s.Username, s.Password)
	}
	return u.String()
}
