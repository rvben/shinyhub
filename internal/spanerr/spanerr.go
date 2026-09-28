// Package spanerr reduces error text to what may leave the process on an
// exported OpenTelemetry span, and records it there.
//
// An error reaching a span can wrap arbitrary subprocess output (a failed uv or
// renv build carries the whole log), a transport error or a database error.
// Such text can run to many kilobytes and can echo a URL carrying credentials,
// for example a package-index URL of the form https://user:token@host/simple.
// Every span in ShinyHub that exports error text goes through this package so
// the bound and the redaction have a single implementation. Callers keep the
// original error; only the span copy is reduced.
package spanerr

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// MaxBytes caps the error text a span exports, including the truncation
// marker.
const MaxBytes = 512

// Message returns err's text reduced by Text.
func Message(err error) string {
	return Text(err.Error())
}

// Text reduces error text to its first line, with URL userinfo masked, capped
// at MaxBytes without splitting a UTF-8 sequence.
func Text(s string) string {
	msg := s
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		msg = s[:i]
	}
	msg = RedactURLUserinfo(msg)
	if len(msg) <= MaxBytes {
		return msg
	}
	const marker = "..."
	cut := MaxBytes - len(marker)
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + marker
}

// Record marks span as failed with err: an "exception" event carrying err's
// type and its reduced message, and an Error status with the same message. It
// stands in for span.RecordError, which would export err.Error() verbatim.
func Record(span trace.Span, err error) {
	msg := Message(err)
	span.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.type", fmt.Sprintf("%T", err)),
		attribute.String("exception.message", msg),
	))
	span.SetStatus(codes.Error, msg)
}

// RedactURLUserinfo masks the userinfo component of every URL in a
// (possibly space-separated, e.g. UV_INDEX) value: "https://u:p@host/x"
// becomes "https://***@host/x". Tokens without userinfo pass unchanged.
func RedactURLUserinfo(val string) string {
	tokens := strings.Fields(val)
	for i, tok := range tokens {
		scheme, rest, ok := strings.Cut(tok, "://")
		if !ok {
			continue
		}
		// Userinfo ends at the first "@" before the first "/" of the authority.
		authorityEnd := len(rest)
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			authorityEnd = slash
		}
		if at := strings.LastIndexByte(rest[:authorityEnd], '@'); at >= 0 {
			tokens[i] = scheme + "://***@" + rest[at+1:]
		}
	}
	if len(tokens) == 0 {
		return val
	}
	return strings.Join(tokens, " ")
}

// urlPattern matches a URL in free-form text, stopping at whitespace and the
// quotes and brackets tools wrap URLs in. A comma does not end it: a query
// value may contain one, so a comma-separated list is split before redaction.
var urlPattern = regexp.MustCompile("[A-Za-z][A-Za-z0-9+.-]*://[^\\s'\"`<>()]+")

// RedactURLs masks, in every URL of s, the userinfo and each query value:
// "https://u:p@host/x?token=t" becomes "https://***@host/x?token=***". A
// registry token commonly travels in the query string, where userinfo
// redaction cannot reach it; parameter names and fragments stay, so the URL
// remains recognizable.
func RedactURLs(s string) string {
	return urlPattern.ReplaceAllStringFunc(s, func(u string) string {
		u = RedactURLUserinfo(u)
		base, query, ok := strings.Cut(u, "?")
		if !ok {
			return u
		}
		query, frag, hasFrag := strings.Cut(query, "#")
		params := strings.Split(query, "&")
		for i, param := range params {
			if name, _, ok := strings.Cut(param, "="); ok {
				params[i] = name + "=***"
			} else if param != "" {
				params[i] = "***"
			}
		}
		u = base + "?" + strings.Join(params, "&")
		if hasFrag {
			u += "#" + frag
		}
		return u
	})
}
