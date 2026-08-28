package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeRuntime_RunOnce_ExitsAndCapturesCode(t *testing.T) {
	rt := NewNativeRuntime()
	var buf bytes.Buffer
	p := StartParams{
		Slug: "x", Dir: t.TempDir(),
		Command: []string{"sh", "-c", "echo hello; exit 7"},
	}
	info, err := rt.RunOnce(context.Background(), p, &buf)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 7 {
		t.Fatalf("expected exit code 7, got %d", info.Code)
	}
	if info.Signaled {
		t.Fatal("expected not signaled")
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("expected log to contain hello, got %q", buf.String())
	}
}

func TestTerminateOneShotProcessGroupKillsChildAfterRootExits(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `trap 'exit 0' TERM; /bin/sh -c 'trap "" TERM; while :; do sleep 1; done' & while :; do sleep 1; done`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()
	if err := terminateOneShotProcessGroup(cmd.Process.Pid, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	<-waited
	if err := syscall.Kill(-cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("one-shot process group still exists: %v", err)
	}
}

func TestNativeRuntime_RunOnce_TimeoutKills(t *testing.T) {
	rt := NewNativeRuntime()
	var buf bytes.Buffer
	p := StartParams{
		Slug: "x", Dir: t.TempDir(),
		Command: []string{"sh", "-c", "sleep 30"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	info, err := rt.RunOnce(ctx, p, &buf)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RunOnce returned err for cancelled run: %v", err)
	}
	if !info.Signaled {
		t.Fatalf("expected Signaled=true, got %+v", info)
	}
	if elapsed > 11*time.Second {
		t.Fatalf("RunOnce took %v — grace + kill should be under 11s", elapsed)
	}
}

func TestNativeRuntime_RunOnce_InheritsLifetimeFiles(t *testing.T) {
	rt := NewNativeRuntime()
	lifetimeFile, err := os.OpenFile(filepath.Join(t.TempDir(), "publication.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lifetimeFile.Close()
	var logs bytes.Buffer
	info, err := rt.RunOnce(context.Background(), StartParams{
		Slug:          "writer",
		Dir:           t.TempDir(),
		Command:       []string{"sh", "-c", "printf inherited >&3"},
		LifetimeFiles: []*os.File{lifetimeFile},
	}, &logs)
	if err != nil || info.Code != 0 {
		t.Fatalf("RunOnce info=%+v err=%v logs=%q", info, err, logs.String())
	}
	if _, err := lifetimeFile.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(lifetimeFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "inherited" {
		t.Fatalf("lifetime descriptor contents=%q, want inherited", contents)
	}
}

// TestNativeRuntime_RunOnce_InjectsAppDataEnv guards the contract that
// SHINYHUB_APP_DATA is present in the child env on the one-shot (schedule)
// execution path whenever StartParams.AppDataPath is set. Regressing this
// causes scheduled jobs to lose access to their persistent data dir.
func TestNativeRuntime_RunOnce_InjectsAppDataEnv(t *testing.T) {
	rt := NewNativeRuntime()
	appData := t.TempDir()
	var buf bytes.Buffer
	p := StartParams{
		Slug: "x", Dir: t.TempDir(),
		Command:     []string{"sh", "-c", "printf %s \"$SHINYHUB_APP_DATA\""},
		AppDataPath: appData,
	}
	info, err := rt.RunOnce(context.Background(), p, &buf)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 0 {
		t.Fatalf("exit=%d output=%q", info.Code, buf.String())
	}
	if got := buf.String(); got != appData {
		t.Errorf("SHINYHUB_APP_DATA in child = %q, want %q", got, appData)
	}
}

// TestNativeRuntime_RunOnce_PlatformOverridesUserAppDataEnv verifies the
// platform value (from p.AppDataPath) wins over a user-supplied
// SHINYHUB_APP_DATA in p.Env. os/exec resolves duplicate keys by last
// occurrence, so the runtime must append the platform value last.
func TestNativeRuntime_RunOnce_PlatformOverridesUserAppDataEnv(t *testing.T) {
	rt := NewNativeRuntime()
	appData := t.TempDir()
	var buf bytes.Buffer
	p := StartParams{
		Slug: "x", Dir: t.TempDir(),
		Command:     []string{"sh", "-c", "printf %s \"$SHINYHUB_APP_DATA\""},
		Env:         []string{"SHINYHUB_APP_DATA=/evil"},
		AppDataPath: appData,
	}
	info, err := rt.RunOnce(context.Background(), p, &buf)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 0 {
		t.Fatalf("exit=%d output=%q", info.Code, buf.String())
	}
	if got := buf.String(); got != appData {
		t.Errorf("SHINYHUB_APP_DATA = %q, want %q (platform must win over user env)", got, appData)
	}
}

// TestNativeRuntime_RunOnce_OmitsAppDataEnvWhenUnset verifies that the
// runtime does not invent a SHINYHUB_APP_DATA when p.AppDataPath is empty.
// "unset" must remain distinguishable from "empty string".
func TestNativeRuntime_RunOnce_OmitsAppDataEnvWhenUnset(t *testing.T) {
	rt := NewNativeRuntime()
	var buf bytes.Buffer
	p := StartParams{
		Slug: "x", Dir: t.TempDir(),
		// Use ${VAR+set} to distinguish unset from empty.
		Command: []string{"sh", "-c", "printf %s \"${SHINYHUB_APP_DATA+set}\""},
	}
	info, err := rt.RunOnce(context.Background(), p, &buf)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 0 {
		t.Fatalf("exit=%d output=%q", info.Code, buf.String())
	}
	if got := buf.String(); got != "" {
		t.Errorf("SHINYHUB_APP_DATA should be unset, got %q", got)
	}
}

func TestNativeRuntime_RunOnce_SymlinksSharedMounts(t *testing.T) {
	rt := NewNativeRuntime()
	bundleDir := t.TempDir()
	sourceData := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceData, "marker"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	var buf bytes.Buffer
	p := StartParams{
		Slug: "consumer", Dir: bundleDir,
		Command:      []string{"sh", "-c", "cat data/shared/fetch/marker"},
		SharedMounts: []SharedMount{{SourceSlug: "fetch", HostPath: sourceData}},
	}
	info, err := rt.RunOnce(context.Background(), p, &buf)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if info.Code != 0 {
		t.Fatalf("expected exit 0, got %d (output=%q)", info.Code, buf.String())
	}
	if buf.String() != "ok" {
		t.Fatalf("expected 'ok', got %q", buf.String())
	}
}
