package httpproxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/wzshiming/requests"
)

func TestAll(t *testing.T) {
	realSer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte("hello"))
	}))

	proxySer := httptest.NewServer(&ProxyHandler{BasicAuth("hello", "world")})

	ps, _ := url.Parse(proxySer.URL)
	ps.User = url.UserPassword("hello", "world")

	resp, err := requests.NewClient().
		SetProxyURLByStr(ps.String()).
		SetLogLevel(requests.LogIgnore).
		NewRequest().
		Get(realSer.URL)

	if err != nil {
		t.Fatal(err.Error())
	}
	if string(resp.Body()) != "hello" || resp.StatusCode() != 201 {
		t.Fatal("error")
	}
}
