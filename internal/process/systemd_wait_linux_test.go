//go:build linux

package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/nativebroker"
	"golang.org/x/sys/unix"
)

func systemdWaitTestProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func TestSystemdExitNotification(t *testing.T) {
	cmd := systemdWaitTestProcess(t)
	w := newSystemdExitWaiter(cmd.Process.Pid)
	defer w.close()
	if w.(*systemdPIDWaiter).fd < 0 {
		t.Fatal("Linux fixture must support pidfd notifications")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- w.wait(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("living worker caused a status refresh: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker exit did not wake the monitor promptly")
	}
	// A detached job descendant could still be alive. The consumed exit hint
	// must not cause repeated immediate status requests while it completes.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if err := w.wait(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("consumed notification caused a busy loop: %v", err)
	}
}

func TestSystemdExitWaitCancellationAndFallback(t *testing.T) {
	cmd := systemdWaitTestProcess(t)
	for _, pid := range []int{cmd.Process.Pid, 0} {
		w := newSystemdExitWaiter(pid)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- w.wait(ctx) }()
		time.Sleep(20 * time.Millisecond)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not wake the monitor")
		}
		w.close()
		w.close()
	}
	w := newSystemdExitWaiter(0)
	defer w.close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := w.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unavailable notifications did not use bounded polling: %v", err)
	}
}

func TestSystemdExitWaitDoesNotLeakDescriptors(t *testing.T) {
	cmd := systemdWaitTestProcess(t)
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		w := newSystemdExitWaiter(cmd.Process.Pid)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
		if err := w.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		cancel()
		w.close()
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil || len(after) > len(before)+1 {
		t.Fatalf("waiter leaked descriptors: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestSystemdExitWaitAcrossUIDs(t *testing.T) {
	if pidText := os.Getenv("SHINYHUB_TEST_WAIT_PID"); pidText != "" {
		pid, err := strconv.Atoi(pidText)
		if err != nil {
			t.Fatal(err)
		}
		w := newSystemdExitWaiter(pid)
		defer w.close()
		if w.(*systemdPIDWaiter).fd < 0 {
			t.Fatal("unprivileged controller cannot monitor another UID")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := w.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("cross-UID fixture requires root in a local test container")
	}
	worker := exec.Command("/bin/sleep", "60")
	worker.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Process.Kill(); _ = worker.Wait() }()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(binary, "-test.run=^TestSystemdExitWaitAcrossUIDs$")
	helper.Env = append(os.Environ(), "SHINYHUB_TEST_WAIT_PID="+strconv.Itoa(worker.Process.Pid))
	helper.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65533, Gid: 65533}}
	if out, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("cross-UID exit monitoring failed: %s %v", out, err)
	}
}

// The fake broker runs only in a local Linux/root test container so the real
// transport's root-peer verification stays enabled. No systemd or app data.
func systemdWaitTestBroker(t *testing.T, respond func(nativebroker.Request) nativebroker.Response) *SystemdRuntime {
	return systemdWaitTestBrokerWithFiles(t, func(req nativebroker.Request, _ []*os.File) nativebroker.Response { return respond(req) })
}

func systemdWaitTestBrokerWithFiles(t *testing.T, respond func(nativebroker.Request, []*os.File) nativebroker.Response) *SystemdRuntime {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("broker transport fixture requires root in a local test container")
	}
	path := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			c, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, nativebroker.MaxMessage)
				control := make([]byte, unix.CmsgSpace(nativebroker.MaxFiles*4))
				n, oob, _, _, err := c.ReadMsgUnix(buf, control)
				if err != nil {
					return
				}
				var files []*os.File
				messages, _ := unix.ParseSocketControlMessage(control[:oob])
				for _, m := range messages {
					fds, _ := unix.ParseUnixRights(&m)
					for _, fd := range fds {
						unix.CloseOnExec(fd)
						f := os.NewFile(uintptr(fd), "synthetic-launch")
						files = append(files, f)
						defer f.Close()
					}
				}
				var req nativebroker.Request
				if json.Unmarshal(buf[:n], &req) != nil {
					return
				}
				raw, _ := json.Marshal(respond(req, files))
				_, _ = c.Write(raw)
			}()
		}
	}()
	return &SystemdRuntime{client: nativebroker.Client{Socket: path}, stats: NewNativeRuntime(), logs: map[string]systemdLog{}}
}

func TestSystemdJobWaitIncludesDescendants(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-complete")
	var child *exec.Cmd
	var leaderExited atomic.Bool
	var statuses atomic.Int32
	rt := systemdWaitTestBrokerWithFiles(t, func(req nativebroker.Request, files []*os.File) nativebroker.Response {
		switch req.Op {
		case "start":
			// Reproduce the real guard and inherited log descriptors; the job's
			// leader exits before its detached descendant writes the marker.
			child = exec.Command("/bin/sh", "-c", "read x <&4; (sleep .3; echo complete > \"$1\") & exit 7", "synthetic-job", marker)
			child.ExtraFiles = files
			if err := child.Start(); err != nil {
				return nativebroker.Response{Error: err.Error()}
			}
			go func() { _ = child.Wait(); leaderExited.Store(true) }()
			return nativebroker.Response{State: &nativebroker.State{Unit: "synthetic-job", PID: child.Process.Pid, Active: true}}
		case "status":
			statuses.Add(1)
			_, err := os.Stat(marker)
			state := &nativebroker.State{Unit: req.Unit, Active: !leaderExited.Load(), Populated: err != nil, Code: 7}
			if state.Active {
				state.PID = child.Process.Pid
			}
			return nativebroker.Response{State: state}
		default:
			return nativebroker.Response{}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exit, err := rt.RunOnce(ctx, StartParams{AppID: 1, Slug: "synthetic", Dir: t.TempDir(), Command: []string{"/bin/true"}}, io.Discard)
	if err != nil || exit.Code != 7 || exit.Signaled {
		t.Fatalf("job exit lost: %+v %v", exit, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("job completed before its descendant")
	}
	if statuses.Load() > 5 {
		t.Fatalf("leader exit caused busy polling: %d status calls", statuses.Load())
	}
}

func TestSystemdJobCancellationStopsWorker(t *testing.T) {
	var child *exec.Cmd
	var stops atomic.Int32
	rt := systemdWaitTestBrokerWithFiles(t, func(req nativebroker.Request, files []*os.File) nativebroker.Response {
		switch req.Op {
		case "start":
			child = exec.Command("/bin/sh", "-c", "read x <&4; exec /bin/sleep 60")
			child.ExtraFiles = files
			if err := child.Start(); err != nil {
				return nativebroker.Response{Error: err.Error()}
			}
			return nativebroker.Response{State: &nativebroker.State{Unit: "synthetic-job", PID: child.Process.Pid, Active: true}}
		case "stop":
			if stops.Add(1) == 1 {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
			return nativebroker.Response{}
		default:
			return nativebroker.Response{State: &nativebroker.State{Unit: req.Unit, PID: child.Process.Pid, Active: true, Populated: true}}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	exit, err := rt.RunOnce(ctx, StartParams{AppID: 1, Slug: "synthetic", Dir: t.TempDir(), Command: []string{"/bin/true"}}, io.Discard)
	if err != nil || !exit.Signaled || stops.Load() < 1 {
		t.Fatalf("cancelled job skipped cleanup: %+v stops=%d err=%v", exit, stops.Load(), err)
	}
}

func TestSystemdMonitorIdleDoesNotPollBroker(t *testing.T) {
	cmd := systemdWaitTestProcess(t)
	var statuses, stops atomic.Int32
	first := make(chan struct{}, 1)
	rt := systemdWaitTestBroker(t, func(req nativebroker.Request) nativebroker.Response {
		if req.Op == "stop" {
			stops.Add(1)
		} else if req.Op == "status" {
			if statuses.Add(1) == 1 {
				first <- struct{}{}
			}
		}
		return nativebroker.Response{State: &nativebroker.State{Unit: req.Unit, PID: cmd.Process.Pid, Active: true}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Wait(ctx, RunHandle{ContainerID: "synthetic-unit"}) }()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("monitor did not inspect its worker")
	}
	time.Sleep(350 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("monitor lost cancellation: %v", err)
	}
	if statuses.Load() != 1 || stops.Load() != 0 {
		t.Fatalf("idle monitor repeated broker calls or stopped a live worker: status=%d stop=%d", statuses.Load(), stops.Load())
	}
}

func TestSystemdMonitorPeriodicVerification(t *testing.T) {
	cmd := systemdWaitTestProcess(t)
	var statuses atomic.Int32
	verified := make(chan struct{}, 2)
	rt := systemdWaitTestBroker(t, func(req nativebroker.Request) nativebroker.Response {
		if req.Op == "status" {
			statuses.Add(1)
			verified <- struct{}{}
		}
		return nativebroker.Response{State: &nativebroker.State{Unit: req.Unit, PID: cmd.Process.Pid, Active: true}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), systemdStatusInterval+5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.Wait(ctx, RunHandle{ContainerID: "synthetic-unit"}) }()
	for i := 0; i < 2; i++ {
		select {
		case <-verified:
		case <-ctx.Done():
			t.Fatal("long-lived worker lost periodic broker verification")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || statuses.Load() != 2 {
		t.Fatalf("unexpected verification behavior: status=%d err=%v", statuses.Load(), err)
	}
}

func TestSystemdMonitorExitRequiresBrokerConfirmation(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "broker unavailable"}[unavailable], func(t *testing.T) {
			cmd := systemdWaitTestProcess(t)
			var exited atomic.Bool
			var stops atomic.Int32
			first := make(chan struct{}, 1)
			rt := systemdWaitTestBroker(t, func(req nativebroker.Request) nativebroker.Response {
				if req.Op == "stop" {
					stops.Add(1)
					return nativebroker.Response{}
				}
				if exited.Load() {
					if unavailable {
						return nativebroker.Response{Error: "synthetic broker failure"}
					}
					return nativebroker.Response{State: &nativebroker.State{Unit: req.Unit}}
				}
				select {
				case first <- struct{}{}:
				default:
				}
				return nativebroker.Response{State: &nativebroker.State{Unit: req.Unit, PID: cmd.Process.Pid, Active: true}}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- rt.Wait(ctx, RunHandle{ContainerID: "synthetic-unit"}) }()
			select {
			case <-first:
			case <-ctx.Done():
				t.Fatal("monitor did not inspect its worker")
			}
			// Allow the monitor to arm its pidfd before ending the process.
			time.Sleep(50 * time.Millisecond)
			exited.Store(true)
			_ = cmd.Process.Kill()
			if unavailable {
				select {
				case err := <-done:
					t.Fatalf("exit hint substituted for broker confirmation: %v", err)
				case <-time.After(150 * time.Millisecond):
				}
				cancel()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if stops.Load() != 0 {
					t.Fatal("unverified worker was stopped")
				}
			} else {
				select {
				case err := <-done:
					if err != nil || stops.Load() != 1 {
						t.Fatalf("physical cleanup was not confirmed: stop=%d err=%v", stops.Load(), err)
					}
				case <-time.After(time.Second):
					t.Fatal("monitor did not confirm worker exit promptly")
				}
			}
		})
	}
}
