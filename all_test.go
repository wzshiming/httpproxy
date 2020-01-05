package httpproxy

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHTTPS(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	proxy := httptest.NewTLSServer(&ProxyHandler{})
	defer proxy.Close()

	cli := &http.Client{
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return url.Parse(proxy.URL)
			},
			TLSClientConfig: proxy.Client().Transport.(*http.Transport).TLSClientConfig,
		},
	}

	resp, err := cli.Get(target.URL + "/https")
	if err != nil {
		t.Fatal(err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.HasSuffix(string(body), "/https") {
		t.Fatal(string(body))
	}
}

func TestConnect(t *testing.T) {

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	proxy := httptest.NewServer(&ProxyHandler{})
	defer proxy.Close()
	dialer, err := NewDialer(proxy.URL)
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

func TestConnectHTTPS(t *testing.T) {

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "check", r.RequestURI)
	}))
	defer target.Close()
	proxy := httptest.NewTLSServer(&ProxyHandler{})
	defer proxy.Close()
	dialer, err := NewDialer(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	dialer.TLSClientConfig = proxy.Client().Transport.(*http.Transport).TLSClientConfig
	dialer.TLSClientConfig.ServerName = "127.0.0.1"

	cli := &http.Client{
		Transport: &http.Transport{
			Dial: dialer.Dial,
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

func TestProxy(t *testing.T) {
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

	resp, err := cli.Get(target.URL + "/proxy")
	if err != nil {
		t.Fatal(err)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.HasSuffix(string(body), "/proxy") {
		t.Fatal(string(body))
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
