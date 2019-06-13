package main

import (
	"flag"
	"net/http"

	"github.com/wzshiming/httpproxy"
)

var address string
var username string
var password string

func init() {
	flag.StringVar(&address, "a", ":8080", "listen on the address")
	flag.StringVar(&username, "u", "", "username")
	flag.StringVar(&password, "p", "", "password")
}

func main() {
	ph := &httpproxy.ProxyHandler{}
	if username != "" {
		ph.Authentication = httpproxy.BasicAuth(username, password)
	}
	http.ListenAndServe(address, ph)
}
