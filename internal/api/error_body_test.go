package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rvben/shinyhub/internal/deployfail"
)

// Error messages can carry text the server did not write: a request field
// name, a filename, a manifest line. Whatever bytes they hold, the body must
// stay a JSON object that decodes back to the message, or the client that most
// needs the reason gets a parse error instead.
var awkwardMessages = []string{
	"plain",
	"quote \" and backslash \\",
	"tab\tand newline\n",
	"delete\x7fcharacter",
	"bell\a and vertical tab\v",
	"invalid utf-8 \xff\xfe",
	"line separator  ",
	"<script>&amp;",
}

func TestWriteErrorWritesValidJSONForAnyMessage(t *testing.T) {
	for _, msg := range awkwardMessages {
		t.Run(fmt.Sprintf("%q", msg), func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeError(rec, http.StatusBadRequest, msg)
			assertErrorBody(t, rec, http.StatusBadRequest, msg)
		})
	}
}

func TestWriteErrorWithKindWritesValidJSONForAnyMessage(t *testing.T) {
	for _, msg := range awkwardMessages {
		t.Run(fmt.Sprintf("%q", msg), func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErrorWithKind(rec, http.StatusBadGateway, msg, deployfail.BuildFailed)
			body := assertErrorBody(t, rec, http.StatusBadGateway, msg)
			if body["failure_kind"] != string(deployfail.BuildFailed) {
				t.Fatalf("failure_kind = %q, want %q", body["failure_kind"], deployfail.BuildFailed)
			}
		})
	}
}

func assertErrorBody(t *testing.T, rec *httptest.ResponseRecorder, status int, msg string) map[string]string {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d", rec.Code, status)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if want := replaceInvalidBytes(msg); body["error"] != want {
		t.Fatalf("error = %q, want %q", body["error"], want)
	}
	return body
}

// replaceInvalidBytes is what a JSON client reads back for msg: every byte that
// is not part of valid UTF-8 becomes its own U+FFFD.
func replaceInvalidBytes(msg string) string {
	var b strings.Builder
	for len(msg) > 0 {
		r, size := utf8.DecodeRuneInString(msg)
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(msg[:size])
		}
		msg = msg[size:]
	}
	return b.String()
}
