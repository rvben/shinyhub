package deploy

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWaitHealthy_UsesProvidedTransport(t *testing.T) {
	used := false
	tr := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})
	err := waitHealthy("https://worker.example/v1/data/tok/health/ready", 2*time.Second, tr)
	if err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	if !used {
		t.Error("waitHealthy did not use the provided transport")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
