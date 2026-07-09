package cli

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// When the host answers but is not a shinyhub server (a front proxy 401s
// everything, including /api/server-info, on a half-provisioned box), the
// preflight classifies it as server-not-ready (exit 6) instead of lumping it
// into transport/auth (exit 3) with the misleading "run shinyhub login" hint.
func TestFleetPreflight_ServerNotReadyIsExit6(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"No authentication token"}`))
	})
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")

	var errBuf bytes.Buffer
	pf, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "apply", 0)
	if pf != nil {
		t.Fatalf("pf must be nil on a not-ready server, got %+v", pf)
	}
	if err == nil || exitCode(err) != 6 {
		t.Fatalf("want exit 6 (server-not-ready), got err=%v code=%d\n%s", err, exitCode(err), errBuf.String())
	}
	out := strings.ToLower(errBuf.String())
	if !strings.Contains(out, "not ready") && !strings.Contains(out, "not up yet") {
		t.Errorf("expected a server-not-ready message, got:\n%s", errBuf.String())
	}
	if strings.Contains(out, "shinyhub login") {
		t.Errorf("must NOT print the misleading 'run shinyhub login' hint for a not-ready server, got:\n%s", errBuf.String())
	}
}

// On a healthy shinyhub (server-info is valid) a 401 from the authenticated
// /api/apps call is a real auth failure and keeps exit 3.
func TestFleetPreflight_AuthFailOnHealthyServerIsExit3(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server-info" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"1.0.0","capabilities":{"content_digest":true}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	})
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")

	var errBuf bytes.Buffer
	_, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "apply", 0)
	if err == nil || exitCode(err) != 3 {
		t.Fatalf("want exit 3 (transport/auth) on a healthy server, got err=%v code=%d\n%s", err, exitCode(err), errBuf.String())
	}
}

// An older shinyhub that lacks /api/server-info (404) but is otherwise healthy
// must NOT be misclassified as not-ready: a real auth failure on /api/apps keeps
// exit 3, with the genuine error, not exit 6 "wait for server".
func TestFleetPreflight_OlderServerWithout404ServerInfoIsExit3(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server-info" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	})
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")

	var errBuf bytes.Buffer
	_, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "apply", 0)
	if err == nil || exitCode(err) != 3 {
		t.Fatalf("want exit 3 (auth) on an older server lacking server-info, got err=%v code=%d\n%s", err, exitCode(err), errBuf.String())
	}
}

// A 200 /api/apps whose body decodes as neither the {items} list envelope
// nor a bare array is a protocol mismatch (typically an older CLI against a
// newer server), NOT an auth failure: kind internal / exit 1, no login hint,
// and - when server-info reveals a version different from this CLI's -
// explicit guidance naming both versions.
func TestFleetPreflight_DecodeMismatchIsProtocolNotAuth(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server-info" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"99.0.0","capabilities":{"content_digest":true}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"apps":{"total":1}}`)) // neither envelope nor bare array
	})
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")

	var errBuf bytes.Buffer
	_, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "apply", 0)
	if err == nil {
		t.Fatal("want an error on an undecodable apps response")
	}
	kind, code := classify(err)
	if kind != KindInternal || code != 1 {
		t.Errorf("classify = (%s, %d), want (%s, 1): a decode mismatch is not an auth failure", kind, code, KindInternal)
	}
	out := errBuf.String()
	if strings.Contains(strings.ToLower(out), "shinyhub login") {
		t.Errorf("must NOT print the login hint for a protocol mismatch, got:\n%s", out)
	}
	if !strings.Contains(out, "99.0.0") || !strings.Contains(out, version) {
		t.Errorf("expected guidance naming server version 99.0.0 and client version %s, got:\n%s", version, out)
	}
	if !strings.Contains(strings.ToLower(out), "upgrade") {
		t.Errorf("expected upgrade guidance, got:\n%s", out)
	}
}

// When server-info reports the SAME version as this CLI, the decode failure
// is a genuine protocol bug, not skew: still internal / no login hint, but
// without misleading upgrade advice.
func TestFleetPreflight_DecodeMismatchSameVersionNoUpgradeHint(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/server-info" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"version":"` + version + `","capabilities":{"content_digest":true}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"apps":{"total":1}}`))
	})
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a", "app.py"), "print(1)\n")
	writeFleetManifest(t, dir, "fleet_id=\"eu\"\n\n[[app]]\nslug=\"ops\"\nsource=\"./a\"\n")

	var errBuf bytes.Buffer
	_, err := fleetPreflight(filepath.Join(dir, "shinyhub-fleet.toml"), &errBuf, "apply", 0)
	if err == nil {
		t.Fatal("want an error on an undecodable apps response")
	}
	if kind, code := classify(err); kind != KindInternal || code != 1 {
		t.Errorf("classify = (%s, %d), want (%s, 1)", kind, code, KindInternal)
	}
	out := strings.ToLower(errBuf.String())
	if strings.Contains(out, "upgrade") {
		t.Errorf("same-version mismatch must not advise an upgrade, got:\n%s", errBuf.String())
	}
	if strings.Contains(out, "shinyhub login") {
		t.Errorf("must NOT print the login hint for a protocol mismatch, got:\n%s", errBuf.String())
	}
}

// The --wait-for-server flag is registered on the server-bound commands that
// face the EC2-churn deploy path.
func TestWaitForServerFlagRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"fleet", "plan"},
		{"fleet", "apply"},
		{"deploy"},
	} {
		cmd := commandAtPath(t, path...)
		if cmd.Flags().Lookup("wait-for-server") == nil {
			t.Errorf("%v: expected --wait-for-server flag", path)
		}
	}
}

// commandAtPath walks the real command tree to the command at the given path.
func commandAtPath(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cur := &cobra.Command{Use: "root"}
	AddCommandsTo(cur)
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not found while walking %v", name, path)
		}
		cur = next
	}
	return cur
}
