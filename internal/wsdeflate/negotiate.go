// Package wsdeflate adds RFC 7692 permessage-deflate to a WebSocket whose
// backend does not negotiate it.
//
// R Shiny's server (httpuv) never compresses WebSocket messages, and a Shiny
// session sends its outputs over that socket: a rendered table of a few
// thousand rows is hundreds of kilobytes of JSON that deflate shrinks more
// than tenfold. The proxy answers the client's compression offer on the
// backend's behalf and translates frames in both directions, so the backend
// keeps speaking plain RFC 6455 while the client's link carries compressed
// messages.
//
// The translation uses no context takeover in either direction: every message
// is compressed and decompressed on its own, so the proxy holds no per-socket
// compressor state between messages.
package wsdeflate

import (
	"strconv"
	"strings"
)

// Negotiate chooses a response to the client's Sec-WebSocket-Extensions
// offers. It accepts the first permessage-deflate offer whose parameters are
// all known and satisfiable, and reports false when there is none, in which
// case the connection must be left uncompressed.
//
// The response always requests no context takeover in both directions. Go's
// compressor always uses a 32 KiB window, so an offer that limits the server's
// window below 15 bits cannot be honored and is declined; an offer naming 15
// exactly is echoed, as RFC 7692 section 7.1.2.1 requires of an accepting
// server.
func Negotiate(offers []string) (string, bool) {
	for _, value := range offers {
		for _, offer := range splitOutsideQuotes(value, ',') {
			if resp, ok := acceptOffer(offer); ok {
				return resp, true
			}
		}
	}
	return "", false
}

func acceptOffer(offer string) (string, bool) {
	parts := splitOutsideQuotes(offer, ';')
	if !strings.EqualFold(strings.TrimSpace(parts[0]), "permessage-deflate") {
		return "", false
	}
	seen := make(map[string]bool, len(parts)-1)
	echoServerBits := false
	for _, raw := range parts[1:] {
		name, value, hasValue := strings.Cut(strings.TrimSpace(raw), "=")
		name = strings.ToLower(strings.TrimSpace(name))
		value = unquote(strings.TrimSpace(value))
		if name == "" || seen[name] {
			return "", false
		}
		seen[name] = true
		switch name {
		case "server_no_context_takeover", "client_no_context_takeover":
			if hasValue {
				return "", false
			}
		case "client_max_window_bits":
			// A limit on the client's own window only constrains what the
			// client sends; the inflater accepts any window up to 15 bits.
			if hasValue && !validWindowBits(value) {
				return "", false
			}
		case "server_max_window_bits":
			if !hasValue || value != "15" {
				return "", false
			}
			echoServerBits = true
		default:
			return "", false
		}
	}
	resp := "permessage-deflate; server_no_context_takeover; client_no_context_takeover"
	if echoServerBits {
		resp += "; server_max_window_bits=15"
	}
	return resp, true
}

func validWindowBits(v string) bool {
	if len(v) == 0 || len(v) > 2 || v[0] == '0' {
		return false
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 8 && n <= 15
}

// unquote removes the quotes of an RFC 7230 quoted-string parameter value.
// Window-bit values are digits, so no escape processing is needed; anything
// that is not a plain quoted token stays as-is and fails validation.
func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// splitOutsideQuotes splits s at sep, ignoring separators inside a
// quoted-string.
func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == '\\' && inQuote:
			i++
		case s[i] == sep && !inQuote:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
