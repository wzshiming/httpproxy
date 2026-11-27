package httpproxy

import (
	"encoding/base64"
	"net/url"
	"strings"
	"unsafe"
)

const (
	BasicAuthName         = "Basic"
	ProxyAuthorizationKey = "Proxy-Authorization"
	ProxyAuthenticateKey  = "Proxy-Authenticate"
)

// AuthenticationFunc Authentication interface is implemented
type AuthenticationFunc func(username, password string) bool

// Auth authentication processing
func (f AuthenticationFunc) Auth(username, password string) bool {
	return f(username, password)
}

// Authentication proxy authentication
type Authentication interface {
	Auth(username, password string) bool
}

// BasicAuth HTTP Basic authentication for Header Proxy-Authorization
func BasicAuth(username, password string) Authentication {
	return AuthenticationFunc(func(u, p string) bool {
		return username == u && password == p
	})
}

// BasicAuthFunc HTTP Basic authentication for Header Proxy-Authorization
func BasicAuthFunc(f func(username, password string) bool) Authentication {
	return AuthenticationFunc(f)
}

// parseBasicAuth parses an HTTP Basic Authentication string.
func parseBasicAuth(auth string) (username, password string, ok bool) {
	const prefix = BasicAuthName + " "
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return
	}
	c, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return
	}
	cs := *(*string)(unsafe.Pointer(&c))
	s := strings.IndexByte(cs, ':')
	if s < 0 {
		return
	}
	return cs[:s], cs[s+1:], true
}

// basicAuth HTTP Basic Authentication string.
func basicAuth(u *url.Userinfo) (base string) {
	const prefix = BasicAuthName + " "

	s := u.String()
	base = base64.StdEncoding.EncodeToString(*(*[]byte)(unsafe.Pointer(&s)))

	return prefix + base
}
