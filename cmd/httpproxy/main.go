package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/wzshiming/httpproxy"
)

var address string
var username string
var password string

func init() {
	flag.StringVar(&address, "a", ":8080", "listen on the address")
	flag.StringVar(&username, "u", "", "username")
	flag.StringVar(&password, "p", "", "password")
	flag.Parse()
}

func main() {
	logger := log.New(os.Stderr, "[http proxy] ", log.LstdFlags)
	ph := &httpproxy.ProxyHandler{
		Logger: logger,
	}
	if username != "" {
		ph.Authentication = httpproxy.BasicAuth(username, password)
	}
	err := http.ListenAndServe(address, ph)
	if err != nil {
		logger.Println(err)
	}
}
