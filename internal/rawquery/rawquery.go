// Package rawquery edits platform-owned parameters without re-encoding app URLs.
package rawquery

import (
	"net/url"
	"strings"
)

// Delete removes every occurrence of key, including escaped spellings, while
// preserving all other query components byte for byte.
func Delete(raw, key string) string {
	parts := strings.Split(raw, "&")
	keep := parts[:0]
	for _, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(name)
		if err == nil && decoded == key {
			continue
		}
		keep = append(keep, part)
	}
	return strings.Join(keep, "&")
}

// Set replaces only key, appending its escaped value to the remaining query.
func Set(raw, key, value string) string {
	raw = Delete(raw, key)
	component := url.QueryEscape(key) + "=" + url.QueryEscape(value)
	if raw == "" {
		return component
	}
	return raw + "&" + component
}
