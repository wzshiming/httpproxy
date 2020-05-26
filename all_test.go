package httpproxy

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var (
	tlsConfig1 = func() *tls.Config {
		s := httptest.NewTLSServer(nil)
		defer s.Close()
		t := s.TLS
		return &tls.Config{
			Certificates:             t.Certificates,
			RootCAs:                  t.RootCAs,
			NextProtos:               []string{"http/1.1"},
			PreferServerCipherSuites: true,
			InsecureSkipVerify:       true,
		}
	}()
	tlsConfig2 = func() *tls.Config {
		t := tlsConfig1.Clone()
		t.NextProtos = append([]string{http2.NextProtoTLS}, t.NextProtos...)
		return t
	}()
)

func echoHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "check", r.Proto, r.RequestURI)
}

type TestServer struct {
	Type     string
	Server   *http.Server
	Listener net.Listener
	TLS      *tls.Config
	URL      string
}

func httpServer(handler http.Handler) *TestServer {
	s := &http.Server{
		Handler: handler,
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	go s.Serve(l)

	return &TestServer{
		Type:     "http",
		Server:   s,
		Listener: l,
		URL:      fmt.Sprintf("http://%s", l.Addr().String()),
	}
}

func httpsServer(handler http.Handler, tlsConfig *tls.Config) *TestServer {
	s := &http.Server{
		TLSConfig: tlsConfig,
		Handler:   handler,
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	go s.ServeTLS(l, "", "")

	return &TestServer{
		Type:     "https",
		Server:   s,
		Listener: l,
		TLS:      tlsConfig,
		URL:      fmt.Sprintf("https://%s", l.Addr().String()),
	}
}
func h2cServer(handler http.Handler) *TestServer {
	h2Server := &http2.Server{}
	s := &http.Server{
		Handler: h2c.NewHandler(handler, h2Server),
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	go s.Serve(l)

	return &TestServer{
		Type:     "h2c",
		Server:   s,
		Listener: l,
		URL:      fmt.Sprintf("http://%s", l.Addr().String()),
	}
}

func h2Server(handler http.Handler, tlsConfig *tls.Config) *TestServer {
	h2Server := &http2.Server{}
	s := &http.Server{
		TLSConfig: tlsConfig,
		Handler:   handler,
	}

	err := http2.ConfigureServer(s, h2Server)
	if err != nil {
		panic(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	go s.ServeTLS(l, "", "")

	return &TestServer{
		Type:     "h2",
		Server:   s,
		Listener: l,
		TLS:      tlsConfig,
		URL:      fmt.Sprintf("https://%s", l.Addr().String()),
	}
}

func proxyH1RoundTripper(proxy *TestServer) http.RoundTripper {
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse(proxy.URL)
		},
		TLSClientConfig: tlsConfig1,
	}
	return transport
}

func connectH1RoundTripper(proxy *TestServer) http.RoundTripper {
	dialer, err := NewDialer(proxy.URL)
	if err != nil {
		panic(err)
	}
	if dialer.TLSClientConfig != nil {
		dialer.TLSClientConfig = tlsConfig1
	}
	transport := &http.Transport{
		DialContext:     dialer.DialContext,
		TLSClientConfig: tlsConfig1,
	}
	return transport
}

func proxyH2RoundTripper(proxy *TestServer) http.RoundTripper {
	transport := &http.Transport{
		Proxy: func(*http.Request) (*url.URL, error) {
			return url.Parse(proxy.URL)
		},
		TLSClientConfig:   tlsConfig2,
		ForceAttemptHTTP2: true,
	}
	err := http2.ConfigureTransport(transport)
	if err != nil {
		panic(err)
	}
	return transport
}

func connectH2RoundTripper(proxy *TestServer) http.RoundTripper {
	dialer, err := NewDialer(proxy.URL)
	if err != nil {
		panic(err)
	}
	if dialer.TLSClientConfig != nil {
		dialer.TLSClientConfig = tlsConfig2
	}
	transport := &http.Transport{
		DialContext:       dialer.DialContext,
		TLSClientConfig:   tlsConfig2,
		ForceAttemptHTTP2: true,
	}
	err = http2.ConfigureTransport(transport)
	if err != nil {
		panic(err)
	}
	return transport
}

var targets = []*TestServer{
	httpServer(http.HandlerFunc(echoHandler)),
	httpsServer(http.HandlerFunc(echoHandler), tlsConfig1.Clone()),
	h2cServer(http.HandlerFunc(echoHandler)),
	h2Server(http.HandlerFunc(echoHandler), tlsConfig2.Clone()),
}

var proxys = []*TestServer{
	httpServer(&ProxyHandler{}),
	httpsServer(&ProxyHandler{}, tlsConfig1.Clone()),
	h2cServer(&ProxyHandler{}),
	h2Server(&ProxyHandler{}, tlsConfig1.Clone()),
}

type transport struct {
	name         string
	roundTripper func(proxy *TestServer) http.RoundTripper
}

var transports = []transport{
	{
		name:         "proxy_h1",
		roundTripper: proxyH1RoundTripper,
	},
	{
		name:         "connect_h1",
		roundTripper: connectH1RoundTripper,
	},
	{
		name:         "proxy_h2",
		roundTripper: proxyH2RoundTripper,
	},
	{
		name:         "connect_h2",
		roundTripper: connectH2RoundTripper,
	},
}

func TestAllProxy(t *testing.T) {
	for _, target := range targets {
		for _, proxy := range proxys {
			for _, client := range transports {
				name := fmt.Sprintf("%s_%s_%s", target.Type, proxy.Type, client.name)
				t.Run(name, func(t *testing.T) {
					roundTripper := client.roundTripper(proxy)

					cli := http.Client{
						Transport: roundTripper,
					}

					resp, err := cli.Get(target.URL + "/" + name)
					if err != nil {
						t.Fatal(err)
					}

					body, err := ioutil.ReadAll(resp.Body)
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()

					b := strings.TrimSpace(string(body))
					if !strings.HasSuffix(b, "/"+name) {
						t.Fatal(string(body))
					}
					t.Log(b)
				})
			}
		}
	}
}

func TestConnectAuth(t *testing.T) {

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	proxy := httptest.NewServer(&ProxyHandler{Authentication: BasicAuth("username", "password")})
	defer proxy.Close()

	u, _ := url.Parse(proxy.URL)
	u.User = url.UserPassword("username", "password")
	dialer, err := NewDialer(u.String())
	if err != nil {
		t.Fatal(err)
	}

	cli := &http.Client{
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}

	resp, err := cli.Get(target.URL + "/connect")
	if err != nil {
		t.Fatal(err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.HasSuffix(string(body), "/connect") {
		t.Fatal(string(body))
	}
}

func TestAuth(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	var proxy = httptest.NewServer(&ProxyHandler{Authentication: BasicAuth("username", "password")})
	defer proxy.Close()
	purl, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	purl.User = url.UserPassword("username", "password")

	cli := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(purl),
		},
	}

	resp, err := cli.Get(target.URL + "/auth")
	if err != nil {
		t.Fatal(err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.HasSuffix(string(body), "/auth") {
		t.Fatal(string(body))
	}
}

func TestUnauth(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	var proxy = httptest.NewServer(&ProxyHandler{Authentication: BasicAuth("username", "password")})
	defer proxy.Close()
	purl, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	purl.User = url.UserPassword("username", "not pwd")

	cli := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(purl),
		},
	}

	resp, err := cli.Get(target.URL + "/auth")
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatal(resp.StatusCode, http.StatusText(resp.StatusCode))
	}

}

func BenchmarkDirect(b *testing.B) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	cli := &http.Client{
		Transport: &http.Transport{},
	}

	for i := 0; i != b.N; i++ {
		resp, _ := cli.Get(target.URL + "/direct")
		resp.Body.Close()
	}
}

func BenchmarkConnect(b *testing.B) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	proxy := httptest.NewServer(&ProxyHandler{})
	defer proxy.Close()
	dialer, err := NewDialer(proxy.URL)
	if err != nil {
		b.Fatal(err)
	}

	cli := &http.Client{
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}

	for i := 0; i != b.N; i++ {
		resp, _ := cli.Get(target.URL + "/connect")
		resp.Body.Close()
	}
}

func BenchmarkProxy(b *testing.B) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	proxy := httptest.NewServer(&ProxyHandler{})
	defer proxy.Close()
	cli := &http.Client{
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return url.Parse(proxy.URL)
			},
		},
	}
	for i := 0; i != b.N; i++ {
		resp, _ := cli.Get(target.URL + "/proxy")
		resp.Body.Close()
	}
}
