package main

import (
	"flag"
	"fmt"
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
	flag.Parse()
	ph := &httpproxy.ProxyHandler{}
	if username != "" {
		ph.Authentication = httpproxy.BasicAuth(username, password)
	}
	err := http.ListenAndServe(address, ph)
	if err != nil {
		fmt.Println(err)
	}
}
