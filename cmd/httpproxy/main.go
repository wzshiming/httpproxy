package main

import (
	"flag"
	"net/http"

	"github.com/wzshiming/httpproxy"
)

var address string

func init() {
	flag.StringVar(&address, "a", ":8080", "listen on the address")
}

func main() {
	http.ListenAndServe(address, &httpproxy.ProxyHandler{})
}
