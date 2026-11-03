package upgrade

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cloudflare/tableflip"
)

func TestUpgradeAdmissionRetriesStartupAndParentRetirementBeforeGuard(t *testing.T) {
	u := &RepeatableUpgrader{stopped: make(chan struct{}), parentDone: make(chan struct{})}
	guardCalls := 0
	u.guard = func() (func(), error) { guardCalls++; return nil, errTest }
	for _, stage := range []string{"upgrade is not ready", "parent control plane has not retired"} {
		err := u.Upgrade()
		var transition *TransitionError
		if !errors.As(err, &transition) || transition.Reason != stage || guardCalls != 0 {
			t.Fatalf("admission %q: %v, guard calls %d", stage, err, guardCalls)
		}
		u.ready = true
	}
	close(u.parentDone)
	if err := u.Upgrade(); !errors.Is(err, errTest) || guardCalls != 1 {
		t.Fatalf("retired parent: %v, guard calls %d", err, guardCalls)
	}
	close(u.stopped)
	if err := u.Upgrade(); err == nil || guardCalls != 1 {
		t.Fatalf("stopped upgrader admitted guard: %v", err)
	}
}

// The subprocess harness uses real inherited listeners, pipes and held tunnels.
// Its only ownership signal is the actual handoff protocol; no synthetic parent
// channel is closed by the test.
func TestRepeatableProcessHelper(t *testing.T) {
	if os.Getenv("RELAY_TEST_REEXEC") != "1" {
		return
	}
	var u Upgrader
	var repeat *RepeatableUpgrader
	var err error
	legacy := os.Getenv("RELAY_TEST_LEGACY") == "parent" && !hasTestParent() || os.Getenv("RELAY_TEST_LEGACY") == "child" && hasTestParent()
	if legacy {
		var old *tableflip.Upgrader
		old, err = tableflip.New(tableflip.Options{UpgradeTimeout: time.Second, PIDFile: os.Getenv("RELAY_TEST_PID")})
		if err == nil {
			u = old
		}
	} else {
		repeat, err = NewRepeatable(time.Second, os.Getenv("RELAY_TEST_PID"))
		u = repeat
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if repeat != nil {
			repeat.Close()
		} else {
			u.Stop()
		}
	}()
	if hasTestParent() && os.Getenv("RELAY_TEST_FAIL_CHILD") == "1" {
		// Simulate a failed child that published its PID but never acknowledged.
		if err := os.WriteFile(os.Getenv("RELAY_TEST_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		select {}
	}
	ln, err := u.Listen("tcp", os.Getenv("RELAY_TEST_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	var sessions sync.WaitGroup
	ready := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/upgrade":
			if err := u.Upgrade(); err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
			fmt.Fprint(w, "upgraded")
		case "/hold":
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			sessions.Add(1)
			defer sessions.Done()
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			rw.Flush()
			for {
				line, err := rw.ReadString('\n')
				if err != nil {
					return
				}
				fmt.Fprintf(rw, "%d:%s", os.Getpid(), line)
				if rw.Flush() != nil {
					return
				}
			}
		default:
			select {
			case <-ready:
			default:
				http.Error(w, "process is not ready", http.StatusServiceUnavailable)
				return
			}
			// A successor can accept from the inherited listener before the
			// parent has stopped accepting. Publish its PID only after the real
			// retirement acknowledgement, so later test requests target a
			// single serving generation rather than racing the draining parent.
			if repeat != nil {
				select {
				case <-repeat.parentDone:
				default:
					http.Error(w, "parent is retiring", http.StatusServiceUnavailable)
					return
				}
			}
			fmt.Fprint(w, os.Getpid())
		}
	})
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	if repeat != nil {
		repeat.EnableRelayHandoff()
	}
	if err := u.Ready(); err != nil {
		t.Fatal(err)
	}
	close(ready)
	<-u.Exit()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if repeat != nil {
		repeat.RetireControlPlane()
	}
	// Upgrade's response can arrive before Shutdown closes the parent listener.
	// Publish actual listener retirement so a strict follow-up upgrade request
	// cannot be accepted, then dropped, by the retiring parent's HTTP server.
	retired := os.Getenv("RELAY_TEST_PID") + ".retired." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(retired, nil, 0600); err != nil {
		t.Fatal(err)
	}
	sessions.Wait()
}
func hasTestParent() bool { return os.Getenv(inheritedEnv) != "" }

func startUpgradeHarness(t *testing.T, legacy string, failChild bool) (string, *http.Client, *exec.Cmd) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	dir := t.TempDir()
	log, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRepeatableProcessHelper$")
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, inheritedEnv+"=") && !strings.HasPrefix(item, relayAwareEnv+"=") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "RELAY_TEST_REEXEC=1", "RELAY_TEST_LEGACY="+legacy, "RELAY_TEST_ADDR="+addr, "RELAY_TEST_PID="+filepath.Join(dir, "pid"))
	if failChild {
		cmd.Env = append(cmd.Env, "RELAY_TEST_FAIL_CHILD=1")
	}
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
		log.Close()
		if t.Failed() {
			data, _ := os.ReadFile(log.Name())
			t.Logf("process log: %s", data)
		}
	})
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 2 * time.Second}
	t.Cleanup(func() { client.CloseIdleConnections() })
	url := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			return url, client, cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subprocess never became ready")
	return "", nil, nil
}
func readHarnessPID(t *testing.T, client *http.Client, url string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if resp.StatusCode == http.StatusOK {
				pid, err := strconv.Atoi(string(body))
				if err != nil {
					t.Fatalf("PID request: %d %q %v", resp.StatusCode, body, err)
				}
				return pid
			}
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("PID request: %d %q", resp.StatusCode, body)
			}
		} else if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		// During handoff, Shutdown can close a StateNew connection accepted
		// by the retiring server before it reads this request. Retry the read-only
		// probe's EOF or explicit readiness status; upgrade requests and held
		// tunnels stay strict.
		if !time.Now().Before(deadline) {
			t.Fatal("PID probe did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitHarnessSuccessor(t *testing.T, client *http.Client, url string, parentPID int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		pid := readHarnessPID(t, client, url)
		if pid != parentPID {
			return pid
		}
		// Upgrade acknowledges successor readiness before the parent finishes
		// Shutdown. A final parent response does not mean takeover failed.
		if !time.Now().Before(deadline) {
			t.Fatal("successor did not take over")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitHarnessListenerRetirement(t *testing.T, process *exec.Cmd, pid int) {
	t.Helper()
	var marker string
	for _, item := range process.Env {
		if path, ok := strings.CutPrefix(item, "RELAY_TEST_PID="); ok {
			marker = path + ".retired." + strconv.Itoa(pid)
		}
	}
	if marker == "" {
		t.Fatal("harness PID path is missing")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if !time.Now().Before(deadline) {
			t.Fatal("parent listener did not retire")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReadHarnessPIDRetriesRetiringConnection(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		case 2:
			http.Error(w, "parent is retiring", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, "42")
	}))
	defer srv.Close()
	if pid := readHarnessPID(t, srv.Client(), srv.URL); pid != 42 {
		t.Fatalf("PID = %d, want successor PID 42", pid)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want closed connection, retiring parent, then ready successor", got)
	}
}
func holdHarnessTunnel(t *testing.T, url string, pid int) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(url, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(conn)
	fmt.Fprint(conn, "GET /hold HTTP/1.1\r\nHost: test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != 101 {
		conn.Close()
		t.Fatalf("hold response: %v %v", resp, err)
	}
	t.Cleanup(func() { conn.Close() })
	checkHarnessTunnel(t, conn, reader, pid)
	return conn, reader
}
func checkHarnessTunnel(t *testing.T, conn net.Conn, reader *bufio.Reader, pid int) {
	t.Helper()
	fmt.Fprint(conn, "ping\n")
	line, err := reader.ReadString('\n')
	if err != nil || line != fmt.Sprintf("%d:ping\n", pid) {
		t.Fatalf("retained tunnel: %q %v", line, err)
	}
}
func TestRepeatableUpgradesPreserveEarlierTunnels(t *testing.T) {
	url, client, _ := startUpgradeHarness(t, "", false)
	var connections []net.Conn
	var readers []*bufio.Reader
	var pids []int
	for generation := 0; generation < 4; generation++ {
		pid := readHarnessPID(t, client, url)
		conn, reader := holdHarnessTunnel(t, url, pid)
		connections = append(connections, conn)
		readers = append(readers, reader)
		pids = append(pids, pid)
		if generation < 3 {
			deadline := time.Now().Add(3 * time.Second)
			succeeded := false
			for time.Now().Before(deadline) {
				resp, err := client.Get(url + "/upgrade")
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					succeeded = true
					break
				}
				if !strings.Contains(string(body), "parent control plane has not retired") {
					t.Fatalf("upgrade: %d %s", resp.StatusCode, body)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !succeeded {
				t.Fatal("successor remained fenced by retained tunnel")
			}
			waitHarnessSuccessor(t, client, url, pid)
		}
		for i := range connections {
			checkHarnessTunnel(t, connections[i], readers[i], pids[i])
		}
	}
}
func TestRepeatableFirstUpgradeFromTableflip(t *testing.T) {
	url, client, _ := startUpgradeHarness(t, "parent", false)
	pid := readHarnessPID(t, client, url)
	resp, err := client.Get(url + "/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("legacy handoff: %s", body)
	}
	waitHarnessSuccessor(t, client, url, pid)
}
func TestRepeatableLegacyChildWaitsForParentExit(t *testing.T) {
	url, client, process := startUpgradeHarness(t, "child", false)
	pid := readHarnessPID(t, client, url)
	conn, reader := holdHarnessTunnel(t, url, pid)
	resp, err := client.Get(url + "/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("legacy child handoff failed")
	}
	checkHarnessTunnel(t, conn, reader, pid)
	// tableflip cannot upgrade again while its parent is still relaying.
	waitHarnessListenerRetirement(t, process, pid)
	resp2, err := client.Get(url + "/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == 200 {
		t.Fatal("legacy successor released before process exit")
	}
}
func TestRepeatableFailedSuccessorKeepsListenerAndTunnel(t *testing.T) {
	url, client, process := startUpgradeHarness(t, "", true)
	pid := readHarnessPID(t, client, url)
	conn, reader := holdHarnessTunnel(t, url, pid)
	resp, err := client.Get(url + "/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("unready successor accepted")
	}
	if readHarnessPID(t, client, url) != pid {
		t.Fatal("failed upgrade replaced parent")
	}
	for _, entry := range process.Env {
		if path, ok := strings.CutPrefix(entry, "RELAY_TEST_PID="); ok {
			data, err := os.ReadFile(path)
			if err != nil || strings.TrimSpace(string(data)) != strconv.Itoa(pid) {
				t.Fatalf("failed child left PID file stale: %s %v", data, err)
			}
		}
	}
	checkHarnessTunnel(t, conn, reader, pid)
}

func TestStopKeepsSuccessorFencedUntilFinalClose(t *testing.T) {
	u, err := NewRepeatable(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	u.retirement = write
	u.upgraded = true
	u.successorAware = false
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, read); close(done) }()
	u.Stop()
	select {
	case <-done:
		t.Fatal("Stop released a legacy successor before drain finished")
	case <-time.After(30 * time.Millisecond):
	}
	if u.RetireControlPlane() {
		t.Fatal("legacy successor allowed early retirement")
	}
	u.Close()
	select {
	case <-done:
		t.Fatal("deferred Close released successor before actual process exit")
	case <-time.After(30 * time.Millisecond):
	}
	write.Close() // Simulate the kernel releasing descriptors at process exit.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("kernel close did not release successor")
	}
}

func TestBeforeReadyPublicationFailureKeepsParentUnacknowledged(t *testing.T) {
	u, err := NewRepeatable(time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	u.SetBeforeReady(func() error { return fmt.Errorf("publication failed") })
	if err := u.Ready(); err == nil {
		t.Fatal("Ready succeeded despite failed publication")
	}
	if u.ready {
		t.Fatal("failed publication marked server ready")
	}
}
