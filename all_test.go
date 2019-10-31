package httpproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

var target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	fmt.Println("check", r.RequestURI)
}))

func TestHTTPCONNECT(t *testing.T) {
	proxy := httptest.NewServer(&ProxyHandler{})
	dialer, err := NewDialer(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	cli := &http.Client{
		Transport: &http.Transport{
			DialContext: dialer.DialContext,
		},
	}

	cli.Get(target.URL + "/connect")
}

func TestHTTPProxy(t *testing.T) {
	proxy := httptest.NewServer(&ProxyHandler{})
	cli := &http.Client{
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				return url.Parse(proxy.URL)
			},
		},
	}

	cli.Get(target.URL + "/proxy")
}

func TestAuth(t *testing.T) {

	var proxy = httptest.NewServer(&ProxyHandler{Authentication: BasicAuth("username", "password")})

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

	cli.Get(target.URL + "/auth")
}
