package main

import (
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/backup"
	shinycli "github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
)

// closedPort returns a port nothing is listening on, so the restore guard's
// listener probe is guaranteed to find nothing and the runtime marker is the
// only signal under test.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// A running server is a state the operator can fix, and the envelope is what
// tells them so: "internal" reads as a fault worth retrying, and a remedy
// naming the Go function RestoreForce is not something anyone can type.
func TestRestoreRunningServerRefusalIsActionable(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "shinyhub.db")
	port := closedPort(t)
	t.Setenv("SHINYHUB_CONFIG", filepath.Join(dir, "absent.yaml"))
	t.Setenv("SHINYHUB_DB_DSN", dsn)
	t.Setenv("SHINYHUB_APPS_DIR", filepath.Join(dir, "apps"))
	t.Setenv("SHINYHUB_APP_DATA_DIR", filepath.Join(dir, "app-data"))
	t.Setenv("SHINYHUB_AUTH_SECRET", "test-secret-for-maintenance-commands")
	t.Setenv("SHINYHUB_SERVER_PORT", strconv.Itoa(port))

	cfg, err := config.LoadForMaintenance(serverConfigPath())
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	clear, err := backup.PublishRuntimeMarker(cfg)
	if err != nil {
		t.Fatalf("PublishRuntimeMarker: %v", err)
	}
	defer clear()

	root := buildRoot()
	root.SetArgs([]string{"restore", filepath.Join(dir, "snap.tar.gz")})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err = root.Execute()
	if err == nil {
		t.Fatal("restore proceeded while a server was published as live")
	}
	if !strings.Contains(err.Error(), "stop the server first") {
		t.Errorf("message = %q, does not say what to do", err.Error())
	}

	var ece *shinycli.ExitCodeError
	if !errors.As(err, &ece) {
		t.Fatalf("error carries no kind (%T); it falls through to the internal catch-all", err)
	}
	if ece.Kind != shinycli.KindValidation {
		t.Errorf("kind = %q, want %q", ece.Kind, shinycli.KindValidation)
	}
	var hinted interface{ Hint() string }
	if !errors.As(err, &hinted) {
		t.Fatal("refusal carries no hint")
	}
	if !strings.Contains(hinted.Hint(), "--force") {
		t.Errorf("hint = %q, does not name the flag that overrides the guard", hinted.Hint())
	}
	if strings.Contains(err.Error()+hinted.Hint(), "RestoreForce") {
		t.Error("the remedy names a Go function instead of something the operator can type")
	}
}
