package spanerr_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rvben/shinyhub/internal/spanerr"
)

func TestText_FirstLineRedactedAndBounded(t *testing.T) {
	const secret = "tok-s3cret"
	cases := map[string]string{
		"credential on a later line": "uv sync failed\nerror: https://user:" + secret + "@idx.example/simple returned 401",
		"credential in a long line":  "fetch https://user:" + secret + "@idx.example/simple failed: " + strings.Repeat("x", 4000),
		"multibyte at the cut":       "https://user:" + secret + "@idx.example " + strings.Repeat("é", 1000),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := spanerr.Text(in)
			if strings.Contains(got, secret) {
				t.Fatalf("secret exported: %q", got)
			}
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("newline exported: %q", got)
			}
			if len(got) > spanerr.MaxBytes {
				t.Fatalf("len = %d, want <= %d", len(got), spanerr.MaxBytes)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("invalid UTF-8: %q", got)
			}
		})
	}
}

func TestText_CarriageReturnEndsFirstLine(t *testing.T) {
	for _, in := range []string{"build failed\rprogress 50%", "build failed\r\nprogress 50%"} {
		if got := spanerr.Text(in); got != "build failed" {
			t.Fatalf("Text(%q) = %q, want %q", in, got, "build failed")
		}
	}
}

func TestText_ShortMessageUnchanged(t *testing.T) {
	if got := spanerr.Message(errors.New("dial tcp: connection refused")); got != "dial tcp: connection refused" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactURLUserinfo(t *testing.T) {
	cases := map[string]string{
		"https://u:p@host/x":              "https://***@host/x",
		"https://host/x":                  "https://host/x",
		"https://a:b@one/s https://two/s": "https://***@one/s https://two/s",
		"https://host/path@notuserinfo":   "https://host/path@notuserinfo",
		"":                                "",
		"https://u:p@h:8080":              "https://***@h:8080",
	}
	for in, want := range cases {
		if got := spanerr.RedactURLUserinfo(in); got != want {
			t.Errorf("RedactURLUserinfo(%q) = %q, want %q", in, got, want)
		}
	}
}

// A registry token often travels in a URL query string, where userinfo
// redaction cannot reach it; RedactURLs masks both in every URL of free-form
// text, keeping the surrounding punctuation and the parameter names.
func TestRedactURLs(t *testing.T) {
	cases := map[string]string{
		"error: Failed to fetch: `http://127.0.0.1:9/simple/six/?token=lit-a1`":  "error: Failed to fetch: `http://127.0.0.1:9/simple/six/?token=***`",
		"(https://u:lit-b2@idx.example/simple?k=lit-c3&x=lit-d4#top)":            "(https://***@idx.example/simple?k=***&x=***#top)",
		"https://one.example/s?lit-e5 https://two.example/s?token=lit-f6,lit-g7": "https://one.example/s?*** https://two.example/s?token=***",
		"./wheels and https://plain.example/simple stay as they are":             "./wheels and https://plain.example/simple stay as they are",
	}
	for in, want := range cases {
		if got := spanerr.RedactURLs(in); got != want {
			t.Errorf("RedactURLs(%q) = %q, want %q", in, got, want)
		}
	}
}
