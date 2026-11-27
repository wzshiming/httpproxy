package main

import (
	"crypto/tls"
	"flag"
	"log"
	"os"

	"github.com/wzshiming/httpproxy"
)

var address string
var username string
var password string
var via string
var certFile string
var keyFile string
var forwardedFor bool

func init() {
	flag.StringVar(&address, "a", ":8080", "listen on the address")
	flag.StringVar(&username, "u", "", "username")
	flag.StringVar(&password, "p", "", "password")
	flag.StringVar(&via, "via", "", "name of the proxy to add to the Via header, not added if empty")
	flag.BoolVar(&forwardedFor, "forwarded-for", false, "add the address of the client to the X-Forwarded-For header")
	flag.StringVar(&certFile, "cert", "", "certificate file to serve HTTPS with, together with -key")
	flag.StringVar(&keyFile, "key", "", "private key file to serve HTTPS with, together with -cert")
	flag.Parse()
}

func main() {
	logger := log.New(os.Stderr, "[http proxy] ", log.LstdFlags)
	svc := httpproxy.NewServer()
	svc.Logger = logger
	svc.Via = via
	svc.ForwardedFor = forwardedFor
	if username != "" {
		svc.Authentication = httpproxy.BasicAuth(username, password)
	}
	var err error
	if certFile != "" || keyFile != "" {
		var cert tls.Certificate
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err == nil {
			err = svc.ListenAndServeTLS("tcp", address, &tls.Config{
				Certificates: []tls.Certificate{cert},
			})
		}
	} else {
		err = svc.ListenAndServe("tcp", address)
	}
	if err != nil {
		logger.Println(err)
	}
}
