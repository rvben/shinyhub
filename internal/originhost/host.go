// Package originhost canonicalizes virtual-host boundaries the same way a
// browser does before origin and cookie decisions.
package originhost

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

// Hostname returns a lowercase ASCII hostname without a root dot or port.
func Hostname(authority string) (string, error) {
	u, err := url.Parse("//" + strings.TrimSpace(authority))
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid host")
	}
	raw := strings.TrimSuffix(u.Hostname(), ".")
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String(), nil
	}
	if looksNumericIPv4(raw) {
		return "", fmt.Errorf("non-canonical numeric host")
	}
	ascii, err := idna.Lookup.ToASCII(raw)
	if err != nil || ascii == "" {
		return "", fmt.Errorf("invalid IDNA host")
	}
	ascii = strings.TrimSuffix(strings.ToLower(ascii), ".")
	if ip := net.ParseIP(ascii); ip != nil {
		return ip.String(), nil
	}
	return ascii, nil
}

// Authority includes an explicit non-default HTTPS port.
func Authority(authority string) (string, error) {
	u, err := url.Parse("//" + strings.TrimSpace(authority))
	if err != nil {
		return "", fmt.Errorf("invalid authority")
	}
	host, err := Hostname(authority)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" || port == "443" {
		return host, nil
	}
	return net.JoinHostPort(host, port), nil
}

func looksNumericIPv4(host string) bool {
	if host == "" || !unicode.IsDigit(rune(host[0])) {
		return false
	}
	for _, r := range host {
		if unicode.IsDigit(r) || r == '.' || r == 'x' || r == 'X' || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
