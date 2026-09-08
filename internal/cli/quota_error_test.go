package cli

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/deployfail"
	"github.com/rvben/shinyhub/internal/fleet"
)

func TestQuotaErrorMessagesAndFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		quota            bool
	}{
		{"gib", `{"error":"app disk quota exceeded","used_mb":1216,"quota_mb":1024}`, "using 1.19 GiB of 1 GiB", true},
		{"mib", `{"error":"app disk quota exceeded","used_mb":600,"quota_mb":512}`, "using 600 MiB of 512 MiB", true},
		{"missing measurements", `{"error":"app disk quota exceeded"}`, "disk quota exceeded", true},
		{"invalid measurements", `{"error":"app disk quota exceeded","used_mb":-1,"quota_mb":0}`, "disk quota exceeded", true},
		{"bundle limit", `{"error":"bundle too large"}`, "bundle too large", false},
		{"proxy response", `request entity too large`, "request entity too large", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := httpError("", "deploy", &http.Response{StatusCode: 413, Status: "413 Request Entity Too Large"}, []byte(tc.body))
			if !strings.Contains(err.Error(), tc.want) || isDiskQuotaError(err) != tc.quota {
				t.Fatalf("error=%v", err)
			}
			var status *httpStatusError
			if !errors.As(err, &status) || status.Status != 413 {
				t.Fatalf("HTTP status lost: %v", err)
			}
			if tc.quota && !strings.Contains(err.Error(), "storage.app_quota_mb") {
				t.Fatalf("missing recovery: %v", err)
			}
			if tc.name == "missing measurements" || tc.name == "invalid measurements" {
				if strings.Contains(err.Error(), "using ") {
					t.Fatalf("invented usage: %v", err)
				}
			}
		})
	}
	if err := parseDiskQuotaError("deploy", 500, []byte(`{"error":"app disk quota exceeded","used_mb":1216,"quota_mb":1024}`)); err != nil {
		t.Fatalf("misclassified status: %v", err)
	}
}

func TestFleetQuotaFailureIsActionableWithoutRetryOrProcessLogs(t *testing.T) {
	t.Setenv("CI", "true")
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "app.py"), "print(1)\n")
	posts, logs := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/deploy"):
			posts++
			w.WriteHeader(413)
			io.WriteString(w, `{"error":"app disk quota exceeded","used_mb":1216,"quota_mb":1024}`)
		case strings.HasSuffix(r.URL.Path, "/logs"):
			logs++
			io.WriteString(w, "irrelevant historical crash")
		default:
			io.WriteString(w, `{}`)
		}
	}))
	defer server.Close()
	result := convergeApp(&cliConfig{Host: server.URL}, fleet.AppDiff{Slug: "energy", Action: fleet.ActionUpdateSource, Owned: true}, fleet.AppEntry{Slug: "energy"}, fleet.ObservedApp{}, dir, convergeOpts{retries: 2}, "fleet:demo", io.Discard)
	if result.status != statusFailed || result.mutation != mutationNone || result.attempts != 1 || posts != 1 || logs != 0 {
		t.Fatalf("posts=%d logs=%d result=%+v", posts, logs, result)
	}
	if len(result.attemptsDetail) != 1 || result.attemptsDetail[0].Kind != deployfail.BundleInvalid {
		t.Fatalf("classification changed: %+v", result.attemptsDetail)
	}
	var out bytes.Buffer
	renderApplyReport(&out, "demo", applyOutcome{apps: []applyResult{result}}, false)
	for _, want := range []string{"using 1.19 GiB of 1 GiB", "storage.app_quota_mb", "retained deployments"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	for _, bad := range []string{"shinyhub apps logs", "irrelevant historical crash", `"quota_mb"`, "\x1b"} {
		if strings.Contains(out.String(), bad) {
			t.Fatalf("unexpected %q: %s", bad, out.String())
		}
	}
}
