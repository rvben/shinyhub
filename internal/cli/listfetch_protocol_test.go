package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// TestAppsList_DecodeMismatchIsProtocolWithSkewHint verifies the list-fetch
// decoder gives version skew the same treatment as the fleet commands: an
// undecodable body (here the bare array an older server emits where this CLI
// expects the {items,...} envelope) classifies as internal - never auth - and
// the structured envelope carries a hint naming both versions.
func TestAppsList_DecodeMismatchIsProtocolWithSkewHint(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server-info" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"99.0.0","capabilities":{"content_digest":true}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"slug":"demo"}]`)) // bare array: not the envelope
	})

	_, err := execCLI(t, "apps", "list", "-o", "json")
	if err == nil {
		t.Fatal("want an error on an undecodable list response")
	}
	if kind, code := classify(err); kind != KindInternal || code != 1 {
		t.Errorf("classify = (%s, %d), want (%s, 1)", kind, code, KindInternal)
	}

	var envBuf bytes.Buffer
	if code := reportTo(&envBuf, false, formatTable, err); code != 1 {
		t.Errorf("reportTo exit = %d, want 1", code)
	}
	envelope := envBuf.String()
	if !strings.Contains(envelope, `"kind":"internal"`) {
		t.Errorf("envelope kind must be internal, got:\n%s", envelope)
	}
	if !strings.Contains(envelope, "99.0.0") || !strings.Contains(envelope, version) {
		t.Errorf("envelope must carry the version-skew hint naming both versions, got:\n%s", envelope)
	}
}
