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
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/bundle"
)

// fakeApp is the server's view of one app.
type fakeApp struct {
	Slug          string  `json:"slug"`
	Access        string  `json:"access"`
	ContentDigest string  `json:"content_digest"`
	ManagedBy     *string `json:"managed_by"`
	Replicas      int     `json:"replicas"`
	status        string
}

// fleetFakeServer is a minimal but contract-accurate ShinyHub server: it
// enforces the X-Shinyhub-If-* preconditions exactly as the real server does
// (empty If-Content-Digest = no assertion; If-Managed-By header present even
// empty activates the check, empty value asserts unmanaged).
type fleetFakeServer struct {
	mu         sync.Mutex
	apps       map[string]*fakeApp
	preconds   bool
	nextDigest string // digest a deploy will promote to
	url        string
}

func newFleetFake(preconds bool) *fleetFakeServer {
	return &fleetFakeServer{apps: map[string]*fakeApp{}, preconds: preconds, nextDigest: "sha256:DEPLOYED"}
}

func (s *fleetFakeServer) httptest(t *testing.T) *cliConfig {
	srv := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return &cliConfig{Host: srv.URL, Token: "shk_test"}
}

func (s *fleetFakeServer) precondFail(r *http.Request, a *fakeApp) bool {
	if !s.preconds {
		return false
	}
	if d := r.Header.Get("X-Shinyhub-If-Content-Digest"); d != "" {
		if a == nil || a.ContentDigest != d {
			return true
		}
	}
	if v, ok := r.Header["X-Shinyhub-If-Managed-By"]; ok {
		want := ""
		if len(v) > 0 {
			want = v[0]
		}
		cur := ""
		if a != nil && a.ManagedBy != nil {
			cur = *a.ManagedBy
		}
		if want != cur {
			return true
		}
	}
	return false
}

func (s *fleetFakeServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.URL.Path == "/api/server-info":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capabilities": map[string]bool{"fleet_preconditions": s.preconds, "content_digest": true},
		})

	case r.Method == "GET" && r.URL.Path == "/api/apps":
		list := make([]fakeApp, 0, len(s.apps))
		for _, a := range s.apps {
			list = append(list, *a)
		}
		_ = json.NewEncoder(w).Encode(list)

	case r.Method == "POST" && r.URL.Path == "/api/apps":
		var body struct{ Slug, Name, Access string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := s.apps[body.Slug]; !ok {
			s.apps[body.Slug] = &fakeApp{Slug: body.Slug, Access: body.Access, status: "running"}
		}
		w.WriteHeader(201)

	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
		slug := strings.TrimPrefix(r.URL.Path, "/api/apps/")
		a, ok := s.apps[slug]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"app": map[string]any{"status": a.status}})

	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/deploy"):
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/apps/"), "/deploy")
		a := s.apps[slug]
		if a == nil {
			a = &fakeApp{Slug: slug, status: "running"}
			s.apps[slug] = a
		}
		// Compute the real content digest from the uploaded bundle so that a
		// subsequent apply can detect unchanged source correctly.
		digest := s.nextDigest
		if raw, err := io.ReadAll(r.Body); err == nil {
			mr, merr := http.NewRequest(r.Method, r.URL.String(), bytes.NewReader(raw))
			if merr == nil {
				mr.Header = r.Header
				if mf, _, perr := mr.FormFile("bundle"); perr == nil {
					if data, rerr := io.ReadAll(mf); rerr == nil {
						if zr, zerr := zip.NewReader(bytes.NewReader(data), int64(len(data))); zerr == nil {
							if d, derr := bundle.DigestZipReader(zr); derr == nil {
								digest = d
							}
						}
					}
					mf.Close()
				}
			}
		}
		a.ContentDigest = digest
		a.status = "running"
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"ok"}`))

	case r.Method == "PATCH" && strings.HasSuffix(r.URL.Path, "/access"):
		slug := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/apps/"), "/access")
		a := s.apps[slug]
		if s.precondFail(r, a) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		var b struct{ Access string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.Access = b.Access
		w.WriteHeader(200)

	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
		slug := strings.TrimPrefix(r.URL.Path, "/api/apps/")
		a := s.apps[slug]
		if s.precondFail(r, a) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if mb, present := b["managed_by"]; present {
			if mb == nil {
				a.ManagedBy = nil
			} else {
				s := mb.(string)
				a.ManagedBy = &s
			}
		}
		if rv, present := b["replicas"]; present {
			a.Replicas = int(rv.(float64))
		}
		w.WriteHeader(200)

	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/apps/"):
		slug := strings.TrimPrefix(r.URL.Path, "/api/apps/")
		a := s.apps[slug]
		if s.precondFail(r, a) {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":"precondition failed (re-run plan)"}`))
			return
		}
		delete(s.apps, slug)
		w.WriteHeader(200)

	default:
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}
}

func strp(s string) *string { return &s }

func writeCLIConfig(t *testing.T, fake *fleetFakeServer) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(cliConfig{Host: fake.url, Token: "shk_test"}); err != nil {
		t.Fatal(err)
	}
	return path
}

// applyManifest writes a manifest+source tree, points the CLI at fake, and
// runs `fleet apply` with extra args, returning combined output + error.
func applyManifest(t *testing.T, fake *fleetFakeServer, manifest string, args ...string) (string, error) {
	cfgFile := writeCLIConfig(t, fake)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "src", "app.py"), "print(1)\n")
	mustWrite(t, filepath.Join(dir, "shinyhub-fleet.toml"), manifest)
	full := append([]string{"--config", cfgFile, "fleet", "apply",
		"-f", filepath.Join(dir, "shinyhub-fleet.toml")}, args...)
	return execCLI(t, full...)
}

func TestFleetApply_Acceptance_CreateThenIdempotent(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	man := "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./src\"\nvisibility=\"private\"\n"

	out, err := applyManifest(t, fake, man)
	if err != nil {
		t.Fatalf("first apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "1 created") {
		t.Fatalf("want 1 created:\n%s", out)
	}
	if mb := fake.apps["ops"].ManagedBy; mb == nil || *mb != "fleet:eu" {
		t.Fatalf("marker not stamped: %#v", mb)
	}

	out2, err2 := applyManifest(t, fake, man)
	if err2 != nil {
		t.Fatalf("second apply must be clean: %v\n%s", err2, out2)
	}
	if !strings.Contains(out2, "1 unchanged") || strings.Contains(out2, "created") && !strings.Contains(out2, "0 created") {
		t.Fatalf("second apply must be idempotent (all unchanged):\n%s", out2)
	}
}

func TestFleetApply_Acceptance_PruneRemovesAfterConfirm(t *testing.T) {
	fake := newFleetFake(true)
	_ = fake.httptest(t)
	fake.apps["gone"] = &fakeApp{Slug: "gone", Access: "private",
		ContentDigest: "sha256:OLD", ManagedBy: strp("fleet:eu"), status: "running"}
	man := "fleet_id=\"eu\"\n"

	out, err := applyManifest(t, fake, man, "--prune", "--yes")
	if err != nil {
		t.Fatalf("prune apply: %v\n%s", err, out)
	}
	if _, still := fake.apps["gone"]; still {
		t.Fatalf("prune did not delete the app:\n%s", out)
	}
	if !strings.Contains(out, "1 deleted") {
		t.Fatalf("want 1 deleted:\n%s", out)
	}
}

func TestFleetApply_Acceptance_DegradedPruneRefusedExitsZero(t *testing.T) {
	fake := newFleetFake(false) // server WITHOUT preconditions
	_ = fake.httptest(t)
	fake.apps["gone"] = &fakeApp{Slug: "gone", ManagedBy: strp("fleet:eu"), status: "running"}
	out, err := applyManifest(t, fake, "fleet_id=\"eu\"\n", "--prune", "--yes")
	if err != nil {
		t.Fatalf("degraded prune must exit 0 (skipped, not failed): %v\n%s", err, out)
	}
	if _, still := fake.apps["gone"]; !still {
		t.Fatal("degraded mode must NOT delete the app")
	}
	if !strings.Contains(out, "degraded") {
		t.Fatalf("must explain the degraded refusal:\n%s", out)
	}
}
