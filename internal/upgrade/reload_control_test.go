package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// eventDeadlineContext lets protocol events, rather than wall-clock scheduling,
// expire a request. Children still exercise normal context cancellation propagation.
type eventDeadlineContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newEventDeadlineContext() *eventDeadlineContext {
	return &eventDeadlineContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *eventDeadlineContext) Done() <-chan struct{} { return c.done }
func (c *eventDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (c *eventDeadlineContext) expire() { c.once.Do(func() { close(c.done) }) }

type reloadTestUpgrader struct {
	*fakeUpgrader
	run func() error
}

func (u *reloadTestUpgrader) Upgrade() error { return u.run() }

func TestAcknowledgedReloadRefusalAndSuccess(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	writePID := func(pid int) {
		t.Helper()
		if err := os.WriteFile(pidFile, []byte(fmt.Sprint(pid)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writePID(os.Getpid())
	var ready atomic.Bool
	u := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error {
		if !ready.Load() {
			return errors.New("dashboard: elastic pool")
		}
		return os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid()+1)), 0600)
	}}
	state := &SignalState{}
	stop, err := StartReloadControl(context.Background(), pidFile, time.Second, u, state, func() bool { return true }, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	info, err := os.Stat(reloadSocket(pidFile, os.Getpid()))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	if _, err := RequestReload(context.Background(), pidFile, time.Second, time.Second); err == nil || !strings.Contains(err.Error(), "elastic pool") {
		t.Fatalf("refusal = %v", err)
	}
	if state.SuccessfulHandoff() {
		t.Fatal("refusal recorded as successful handoff")
	}
	successor, err := net.Listen("unix", reloadSocket(pidFile, os.Getpid()+1))
	if err != nil {
		t.Fatal(err)
	}
	defer successor.Close()
	var probes atomic.Int32
	go func() {
		for {
			conn, err := successor.Accept()
			if err != nil {
				return
			}
			var request reloadRequest
			_ = json.NewDecoder(conn).Decode(&request)
			n := probes.Add(1)
			_ = json.NewEncoder(conn).Encode(ReloadResult{PID: os.Getpid() + 1, Ready: n >= 2})
			conn.Close()
		}
	}()
	ready.Store(true)
	result, err := RequestReload(context.Background(), pidFile, 0, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.PID != os.Getpid()+1 || !result.Ready || probes.Load() < 2 || !state.SuccessfulHandoff() {
		t.Fatalf("result = %+v", result)
	}
	stop()
	if _, err := os.Stat(reloadSocket(pidFile, os.Getpid())); !os.IsNotExist(err) {
		t.Fatalf("socket survived retirement: %v", err)
	}
}

func TestAcknowledgedReloadCarriesServingAppsAndRefusesFalseSuccess(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-apps-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	apps := []ReloadApp{{ID: 42, Slug: "rolled-app", MinimumReady: 1}}
	upg := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error {
		return os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid()+1)), 0600)
	}}
	stop, err := StartReloadControl(context.Background(), pidFile, time.Second, upg, &SignalState{}, func() bool { return true }, quietLogger(), ReloadServingCheck{Snapshot: func() ([]ReloadApp, error) { return apps, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	successor, err := net.Listen("unix", reloadSocket(pidFile, os.Getpid()+1))
	if err != nil {
		t.Fatal(err)
	}
	defer successor.Close()
	ctx := newEventDeadlineContext()
	defer ctx.expire()
	var validProbes atomic.Int32
	go func() {
		for {
			conn, err := successor.Accept()
			if err != nil {
				return
			}
			var request reloadRequest
			_ = json.NewDecoder(conn).Decode(&request)
			if len(request.Apps) == 1 && request.Apps[0] == apps[0] {
				// Probe two follows the client's processing of the first blocker.
				if validProbes.Add(1) == 2 {
					ctx.expire()
				}
			}
			_ = json.NewEncoder(conn).Encode(ReloadResult{PID: os.Getpid() + 1, Ready: false, NotReadyReason: "rolled-app: 0 ready replicas after reload, need 1"})
			conn.Close()
		}
	}()
	result, err := RequestReload(ctx, pidFile, 0, time.Second)
	if err == nil || !strings.Contains(err.Error(), "rolled-app") || result.Ready || validProbes.Load() == 0 {
		t.Fatalf("false success: result=%+v err=%v probes=%d", result, err, validProbes.Load())
	}
}

func TestReloadControlChecksServingAppsAfterControlPlaneReady(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-check-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	stop, err := StartReloadControl(context.Background(), pidFile, time.Second, newFakeUpgrader(), &SignalState{}, func() bool { return true }, quietLogger(), ReloadServingCheck{Check: func(apps []ReloadApp) string { return apps[0].Slug + ": no ready replica" }})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	result, err := exchangeReload(context.Background(), reloadSocket(pidFile, os.Getpid()), reloadRequest{Version: 1, Check: true, Apps: []ReloadApp{{ID: 42, Slug: "rolled-app", MinimumReady: 1}}})
	if err != nil || result.Ready || !strings.Contains(result.NotReadyReason, "rolled-app") {
		t.Fatalf("serving readiness = %+v, %v", result, err)
	}
}

func TestReloadAcknowledgementIncludesAppsThatStartedServingWhileWaiting(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-new-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	var completed atomic.Bool
	upg := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error {
		// An already-admitted deployment finishes before handoff is accepted.
		completed.Store(true)
		return os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid()+1)), 0600)
	}}
	original := ReloadApp{ID: 1, Slug: "original", MinimumReady: 1}
	newApp := ReloadApp{ID: 2, Slug: "new-app", MinimumReady: 2}
	stop, err := StartReloadControl(context.Background(), pidFile, time.Second, upg, &SignalState{}, func() bool { return true }, quietLogger(), ReloadServingCheck{Snapshot: func() ([]ReloadApp, error) {
		if completed.Load() {
			// Keep the initial obligation even if it vanishes during the wait.
			return []ReloadApp{newApp}, nil
		}
		return []ReloadApp{original}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	result, err := exchangeReload(context.Background(), reloadSocket(pidFile, os.Getpid()), reloadRequest{Version: 1})
	if err != nil || result.Error != "" {
		t.Fatalf("reload: %+v, %v", result, err)
	}
	if len(result.Apps) != 2 || result.Apps[0] != original || result.Apps[1] != newApp {
		t.Fatalf("successor obligations omit a serving app: %+v", result.Apps)
	}
}

func TestReloadAcknowledgementRechecksWarmFloorAndReportsSnapshotFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot_failure=%t", fail), func(t *testing.T) {
			dir, err := os.MkdirTemp("", "sh-reload-floor-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			pidFile := filepath.Join(dir, "pid")
			var completed atomic.Bool
			upg := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error {
				completed.Store(true)
				return os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid()+1)), 0600)
			}}
			stop, err := StartReloadControl(context.Background(), pidFile, time.Second, upg, &SignalState{}, func() bool { return true }, quietLogger(), ReloadServingCheck{Snapshot: func() ([]ReloadApp, error) {
				minimum := 1
				if completed.Load() {
					if fail {
						return nil, errors.New("store unavailable")
					}
					minimum = 3
				}
				return []ReloadApp{{ID: 1, Slug: "dashboard", MinimumReady: minimum}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			result, err := exchangeReload(context.Background(), reloadSocket(pidFile, os.Getpid()), reloadRequest{Version: 1})
			if err != nil || result.PID != os.Getpid()+1 {
				t.Fatalf("handoff acknowledgement: %+v, %v", result, err)
			}
			if fail {
				if !strings.Contains(result.Error, "handoff accepted") || !strings.Contains(result.Error, "store unavailable") {
					t.Fatalf("lost snapshot failure after handoff: %+v", result)
				}
			} else if result.Error != "" || len(result.Apps) != 1 || result.Apps[0].MinimumReady != 3 {
				t.Fatalf("warm floor changed during wait was not enforced: %+v", result)
			}
		})
	}
}

func TestReloadWaitRetriesOnlyTransitionsAndReportsTimeout(t *testing.T) {
	var calls atomic.Int32
	u := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error {
		if calls.Add(1) < 2 {
			return &TransitionError{Reason: "dashboard: draining generation"}
		}
		return nil
	}}
	state := &SignalState{}
	if err := state.Reload(context.Background(), u, time.Second, quietLogger()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("attempts = %d", calls.Load())
	}
	if err := state.Reload(context.Background(), u, time.Second, quietLogger()); err == nil {
		t.Fatal("retiring process accepted another handoff")
	}
	u.run = func() error { return &TransitionError{Reason: "dashboard: draining generation"} }
	state = &SignalState{}
	if err := state.Reload(context.Background(), u, 10*time.Millisecond, quietLogger()); err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "dashboard") {
		t.Fatalf("timeout = %v", err)
	}
	if state.SuccessfulHandoff() {
		t.Fatal("timed-out request recorded as handoff")
	}
}

func TestReloadConcurrentRequestsAndCancellation(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	u := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error { close(started); <-finish; return errTest }}
	state := &SignalState{}
	result := make(chan error, 1)
	go func() { result <- state.Reload(context.Background(), u, 0, quietLogger()) }()
	<-started
	if err := state.Reload(context.Background(), u, 0, quietLogger()); err == nil {
		t.Fatal("concurrent handoff accepted")
	}
	close(finish)
	<-result
	u.run = func() error { return &TransitionError{Reason: "dashboard: draining generation"} }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := state.Reload(ctx, u, time.Hour, quietLogger()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func TestReloadControlRejectsPublicDirectoryAndMalformedRequest(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	u := newFakeUpgrader()
	dir = filepath.Join(dir, ".reload")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if stop, err := StartReloadControl(context.Background(), pidFile, time.Second, u, &SignalState{}, func() bool { return true }, quietLogger()); err == nil {
		stop()
		t.Fatal("public directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	stop, err := StartReloadControl(context.Background(), pidFile, time.Second, u, &SignalState{}, func() bool { return true }, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	conn, err := net.Dial("unix", reloadSocket(pidFile, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = fmt.Fprintln(conn, `{"version":2,"wait":0}`)
	var buf [1024]byte
	n, err := conn.Read(buf[:])
	if err != nil || !strings.Contains(string(buf[:n]), "unsupported") {
		t.Fatalf("invalid request = %s %v", buf[:n], err)
	}
	if u.upgradeCount() != 0 {
		t.Fatal("malformed request launched a successor")
	}
}

func TestReloadCallerCancellationReleasesWaitAndAdmission(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	u := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error {
		attempts.Add(1)
		return &TransitionError{Reason: "dashboard: draining generation"}
	}}
	state := &SignalState{}
	stop, err := StartReloadControl(context.Background(), pidFile, time.Second, u, state, func() bool { return true }, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := RequestReload(ctx, pidFile, time.Hour, time.Second); done <- err }()
	waitFor(t, func() bool { return attempts.Load() > 0 }, "waiter reached guard")
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled caller got success")
	}
	waitFor(t, func() bool { state.mu.Lock(); defer state.mu.Unlock(); return state.inFlight == nil }, "waiting reload releases admission")
	before := attempts.Load()
	if err := state.Reload(context.Background(), u, 0, quietLogger()); err == nil {
		t.Fatal("expected current blocker")
	}
	if attempts.Load() != before+1 {
		t.Fatal("new reload could not enter admission after cancellation")
	}
}

func TestReloadControlRecoversStaleSocketWithoutReplacingLiveSocketOrFiles(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	path := reloadSocket(pidFile, os.Getpid())
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	old.SetUnlinkOnClose(false)
	old.Close() // Models a crash leaving the PID-named socket on disk.
	start := func() (func(), error) {
		return StartReloadControl(context.Background(), pidFile, time.Second, newFakeUpgrader(), &SignalState{}, func() bool { return true }, quietLogger())
	}
	stop, err := start()
	if err != nil {
		t.Fatalf("stale socket recovery: %v", err)
	}
	defer stop()
	if secondStop, err := start(); err == nil {
		secondStop()
		t.Fatal("replaced a live socket")
	}
	result, err := exchangeReload(context.Background(), path, reloadRequest{Version: 1, Check: true})
	if err != nil || !result.Ready {
		t.Fatalf("original endpoint lost: %+v %v", result, err)
	}
	stop()
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if fileStop, err := start(); err == nil {
		fileStop()
		t.Fatal("replaced a regular file")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
}

func TestReloadWaitHookLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "timeout", "cancel", "immediate"} {
		t.Run(scenario, func(t *testing.T) {
			active, finished, success := false, false, false
			state := &SignalState{}
			state.SetReloadWaitHook(func() func(bool) {
				active = true
				return func(ok bool) { active = false; finished = true; success = ok }
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wait := time.Millisecond
			if scenario == "immediate" {
				wait = 0
			}
			u := &reloadTestUpgrader{fakeUpgrader: newFakeUpgrader(), run: func() error {
				if scenario != "immediate" && !active {
					t.Error("activation pause absent during upgrade attempt")
				}
				switch scenario {
				case "success", "immediate":
					return nil
				case "cancel":
					cancel()
					return &TransitionError{Reason: "drain"}
				case "timeout":
					return &TransitionError{Reason: "drain"}
				default:
					return errors.New("refused")
				}
			}}
			err := state.Reload(ctx, u, wait, quietLogger())
			if scenario == "immediate" {
				if finished || active {
					t.Fatal("immediate reload used wait hook")
				}
				return
			}
			if !finished || active || success != (scenario == "success") {
				t.Fatalf("finished=%t active=%t success=%t", finished, active, success)
			}
			if (err == nil) != (scenario == "success") {
				t.Fatal(err)
			}
		})
	}
}

func TestReloadReadinessTimeoutLogsBlockersInSuccessor(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-log-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	previousPID, successorPID := os.Getpid()+1, os.Getpid()
	if err := os.WriteFile(pidFile, []byte(fmt.Sprint(previousPID)), 0600); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	reason := "token-finops: 0 ready replicas after reload, need 1 (status running)"
	successorUpgrader := newFakeUpgrader()
	ctx := newEventDeadlineContext()
	defer ctx.expire()
	var probes atomic.Int32
	stop, err := StartReloadControl(context.Background(), pidFile, time.Second, successorUpgrader, &SignalState{}, func() bool { return true }, slog.New(slog.NewTextHandler(&logs, nil)), ReloadServingCheck{
		Check: func([]ReloadApp) string {
			// Only expire after the client has recorded the first blocker.
			if probes.Add(1) == 2 {
				ctx.expire()
			}
			return reason
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	predecessor, err := net.Listen("unix", reloadSocket(pidFile, previousPID))
	if err != nil {
		t.Fatal(err)
	}
	defer predecessor.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := predecessor.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		var request reloadRequest
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			done <- err
			return
		}
		if err := os.WriteFile(pidFile, []byte(fmt.Sprint(successorPID)), 0600); err != nil {
			done <- err
			return
		}
		done <- json.NewEncoder(conn).Encode(ReloadResult{PreviousPID: previousPID, PID: successorPID, Apps: []ReloadApp{{ID: 42, Slug: "token-finops", MinimumReady: 1}}})
	}()
	result, err := RequestReload(ctx, pidFile, 0, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || result.Ready || result.NotReadyReason != reason || !strings.Contains(err.Error(), "handoff accepted") {
		t.Fatalf("timeout result = %+v, %v", result, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stop() // Wait for server handlers before inspecting their log output.
	if successorUpgrader.upgradeCount() != 0 {
		t.Fatal("readiness probes or timeout report started another handoff")
	}
	for _, want := range []string{"level=ERROR", "reload handoff accepted but successor readiness timed out", reason} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("server log %q missing %q", logs.String(), want)
		}
	}
}

func TestReloadReadinessTimeoutReportIsSafeForLegacySuccessor(t *testing.T) {
	dir, err := os.MkdirTemp("", "sh-reload-legacy-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	pidFile := filepath.Join(dir, "pid")
	if err := os.Mkdir(filepath.Join(dir, ".reload"), 0700); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid() + 1
	listener, err := net.Listen("unix", reloadSocket(pidFile, pid))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// Match the old wire format: the decoder ignores readiness_failure. A
	// timeout diagnostic must still decode as a probe, never a reload request.
	type legacyRequest struct {
		Version int         `json:"version"`
		Check   bool        `json:"check"`
		Apps    []ReloadApp `json:"apps"`
	}
	requests := make(chan legacyRequest, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request legacyRequest
		if err := json.NewDecoder(conn).Decode(&request); err != nil {
			return
		}
		requests <- request
		_ = json.NewEncoder(conn).Encode(ReloadResult{PID: pid, Ready: true})
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := reloadReadinessFailure(ctx, pidFile, ReloadResult{PID: pid}, "app: no ready replica", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) || result.Ready || result.NotReadyReason != "app: no ready replica" {
		t.Fatalf("legacy timeout result: %+v, %v", result, err)
	}
	select {
	case request := <-requests:
		if request.Version != 1 || !request.Check || len(request.Apps) != 0 {
			t.Fatalf("legacy successor received handoff request: %+v", request)
		}
	default:
		t.Fatal("timeout report did not reach legacy successor")
	}
}
