package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	shinycli "github.com/rvben/shinyhub/internal/cli"
	"github.com/spf13/cobra"
)

// newTestHealthcheckRoot builds a throwaway root distinct from the process-wide
// buildRoot() singleton, registering the same --host persistent flag fresh.
// buildRoot()'s pflag.Flag objects persist for the life of the test binary and
// pflag never clears Flag.Changed once a flag has been set, so two behavior
// tests sharing that singleton would leak "--host was passed" from one test
// into the next.
func newTestHealthcheckRoot() *cobra.Command {
	root := &cobra.Command{Use: "shinyhub"}
	root.AddCommand(newHealthcheckCmd())
	shinycli.AddCommandsTo(root)
	return root
}

func TestCheckReady(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantDetail string
	}{
		{name: "ready", status: http.StatusOK, body: `{"ready":true}`},
		{name: "starting", status: http.StatusServiceUnavailable, body: `{"ready":false,"reason":"starting"}`, wantErr: true, wantDetail: `"reason":"starting"`},
		{name: "empty failure", status: http.StatusBadGateway, wantErr: true, wantDetail: "empty response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			err := checkReady(context.Background(), srv.Client(), srv.URL+"/readyz")
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkReady() error = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("checkReady() error = %q, want detail %q", err, tc.wantDetail)
			}
		})
	}
}

func TestCheckReadyRejectsNonHTTPURL(t *testing.T) {
	err := checkReady(context.Background(), http.DefaultClient, "file:///tmp/readyz")
	if err == nil || !strings.Contains(err.Error(), "absolute http:// or https://") {
		t.Fatalf("checkReady() error = %v, want actionable URL error", err)
	}
}

// TestHealthcheckRejectsHostFlag proves that `shinyhub healthcheck --host ...`
// fails loudly instead of accepting --host and silently ignoring it. --host is
// a persistent flag meant for the developer subcommands that resolve it
// against the saved multi-host credentials store; healthcheck is a standalone
// readiness probe that never consults that store, so a value there would
// otherwise have zero effect on which server gets probed.
func TestHealthcheckRejectsHostFlag(t *testing.T) {
	root := newTestHealthcheckRoot()
	root.SetArgs([]string{"healthcheck", "--host", "prod", "--url", "http://127.0.0.1:0/readyz"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	if err == nil {
		t.Fatal("healthcheck --host succeeded silently; want a loud rejection")
	}
	if !strings.Contains(err.Error(), "--host") {
		t.Errorf("message = %q, does not name the offending flag", err.Error())
	}

	var ece *shinycli.ExitCodeError
	if !errors.As(err, &ece) {
		t.Fatalf("error carries no kind (%T); it falls through to the internal catch-all", err)
	}
	if ece.Kind != shinycli.KindValidation {
		t.Errorf("kind = %q, want %q", ece.Kind, shinycli.KindValidation)
	}
}

// TestHealthcheckWithoutHostFlagStillWorks proves the rejection in
// TestHealthcheckRejectsHostFlag is specific to --host being set, not a
// regression that breaks the ordinary, flagless invocation.
func TestHealthcheckWithoutHostFlagStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root := newTestHealthcheckRoot()
	root.SetArgs([]string{"healthcheck", "--url", srv.URL + "/readyz"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatalf("healthcheck without --host: %v", err)
	}
}
