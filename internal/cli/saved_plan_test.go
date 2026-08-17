package cli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/bundle"
)

func savedPlanFixture(t *testing.T, host string, now time.Time) (savedPlanEnvelope, []byte) {
	t.Helper()
	dir := planTestBundle(t)
	preview, err := buildBundlePreview(dir)
	if err != nil {
		t.Fatal(err)
	}
	plan := deploymentPlan{
		Host: host, Slug: "demo", Visibility: "private", Start: true,
		Remote: deploymentRemotePreview{Exists: true, ResourceRevision: "rev:app:planned"},
		Bundle: preview,
	}
	plan.Plan = newPlanDocument("single-app", "shinyhub apply demo.plan", "app/demo", "ShinyHub will update demo", nil, nil, nil, nil)
	envelope, err := buildSavedPlan(plan, preview.Buffer.Bytes(), 1, defaultPlanLifetime, now)
	if err != nil {
		t.Fatal(err)
	}
	return envelope, append([]byte(nil), preview.Buffer.Bytes()...)
}

func TestSavedPlanRoundTripIsPrivateAndExact(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	envelope, bundleBytes := savedPlanFixture(t, "https://hub.example.com", now)
	path := filepath.Join(t.TempDir(), "demo.plan")
	if err := writeSavedPlan(path, envelope, bundleBytes, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("plan mode = %o, want 600", got)
	}
	loaded, err := readSavedPlan(path, now.Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Envelope.PlanID != envelope.PlanID || !bytes.Equal(loaded.Bundle, bundleBytes) {
		t.Fatal("saved plan did not preserve metadata and exact bundle bytes")
	}
	if err := writeSavedPlan(path, envelope, bundleBytes, false); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("second write error = %v, want safe overwrite guidance", err)
	}
}

func TestSavedPlanRejectsCorruptionAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	envelope, bundleBytes := savedPlanFixture(t, "https://hub.example.com", now)
	path := filepath.Join(t.TempDir(), "demo.plan")
	if err := writeSavedPlan(path, envelope, bundleBytes, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSavedPlan(path, now.Add(time.Hour), false); err == nil {
		t.Fatal("corrupt saved plan was accepted")
	}

	expiredPath := filepath.Join(t.TempDir(), "expired.plan")
	if err := writeSavedPlan(expiredPath, envelope, bundleBytes, false); err != nil {
		t.Fatal(err)
	}
	if _, err := readSavedPlan(expiredPath, envelope.ExpiresAt, false); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expiry error = %v", err)
	}
	if _, err := readSavedPlan(expiredPath, envelope.ExpiresAt, true); err != nil {
		t.Fatalf("offline inspection should allow an expired artifact: %v", err)
	}
}

func TestPlanOutThenApplyUploadsReviewedBytesAndRevision(t *testing.T) {
	dir := planTestBundle(t)
	var uploadedDigest, revision string
	var serverInfoCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/server-info":
			serverInfoCalls.Add(1)
			_, _ = io.WriteString(w, `{"version":"dev","protocol_version":1,"capabilities":{"content_digest":true,"plan_apply":true},"runtimes":{"python":true}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/apps/demo":
			_, _ = io.WriteString(w, `{"app":{"slug":"demo","status":"running","access":"private","content_digest":"sha256:old"},"can_manage":true,"resource_revision":"rev:app:planned"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/demo/deploy":
			revision = r.Header.Get("X-Shinyhub-If-Resource-Revision")
			file, _, err := r.FormFile("bundle")
			if err != nil {
				t.Errorf("bundle: %v", err)
				http.Error(w, "bad bundle", http.StatusBadRequest)
				return
			}
			raw, _ := io.ReadAll(file)
			_ = file.Close()
			zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Errorf("zip: %v", err)
				return
			}
			uploadedDigest, err = bundle.DigestZipReader(zr)
			if err != nil {
				t.Errorf("digest: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"slug":"demo","status":"running","deploy_count":2}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)
	planPath := filepath.Join(t.TempDir(), "demo.plan")
	if stdout, stderr, err := execCLISplit(t, "plan", dir, "--slug", "demo", "--out", planPath, "-o", "table"); err != nil {
		t.Fatalf("plan: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	loaded, err := readSavedPlan(planPath, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	showOut, showErrOut, err := execCLISplit(t, "plan", "show", planPath, "-o", "json")
	if err != nil {
		t.Fatalf("plan show: %v\nstdout=%s\nstderr=%s", err, showOut, showErrOut)
	}
	var shown savedPlanEnvelope
	if err := json.Unmarshal([]byte(showOut), &shown); err != nil || shown.PlanID != loaded.Envelope.PlanID {
		t.Fatalf("offline plan show did not return the verified artifact: err=%v plan_id=%q", err, shown.PlanID)
	}
	plannedDigest := loaded.Envelope.Bundle.Digest
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("raise RuntimeError('changed after review')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, err := execCLISplit(t, "apply", planPath, "-o", "table"); err != nil {
		t.Fatalf("apply: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if uploadedDigest != plannedDigest {
		t.Fatalf("uploaded digest = %q, want reviewed %q", uploadedDigest, plannedDigest)
	}
	if revision != "rev:app:planned" {
		t.Fatalf("resource revision = %q", revision)
	}
	if serverInfoCalls.Load() != 2 {
		t.Fatalf("server-info calls = %d, want plan + apply", serverInfoCalls.Load())
	}
}

func TestApplyTargetMismatchMakesNoRequest(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)
	envelope, bundleBytes := savedPlanFixture(t, "https://other.example.com", time.Now())
	path := filepath.Join(t.TempDir(), "wrong-target.plan")
	if err := writeSavedPlan(path, envelope, bundleBytes, false); err != nil {
		t.Fatal(err)
	}
	_, _, err := execCLISplit(t, "apply", path, "-o", "table")
	if err == nil || !strings.Contains(err.Error(), "targets") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("target mismatch made %d requests", requests.Load())
	}
}

func TestApplyExpectedAbsentConflictNeverDeploys(t *testing.T) {
	var creates, deploys atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/server-info":
			_, _ = io.WriteString(w, `{"version":"dev","protocol_version":1,"capabilities":{"plan_apply":true}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps":
			creates.Add(1)
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":"slug already exists"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deploy"):
			deploys.Add(1)
			http.Error(w, "must not deploy", http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	writeTestCLIConfig(t, srv.URL)
	envelope, bundleBytes := savedPlanFixture(t, srv.URL, time.Now())
	envelope.Target.ExpectedExists = false
	envelope.Target.ExpectedRevision = ""
	envelope.Plan.Remote.Exists = false
	var err error
	envelope.Integrity, err = savedPlanIntegrity(envelope, bundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "new-app.plan")
	if err := writeSavedPlan(path, envelope, bundleBytes, false); err != nil {
		t.Fatal(err)
	}
	_, _, applyErr := execCLISplit(t, "apply", path, "-o", "table")
	if ExitCode(applyErr) != 2 || !strings.Contains(applyErr.Error(), "expected the app to be absent") {
		t.Fatalf("error = %v", applyErr)
	}
	if creates.Load() != 1 || deploys.Load() != 0 {
		t.Fatalf("create/deploy calls = %d/%d, want 1/0", creates.Load(), deploys.Load())
	}
}
