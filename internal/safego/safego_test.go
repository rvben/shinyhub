package safego

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer wraps bytes.Buffer with a mutex. The background goroutine or
// timer callback under test writes to the captured log concurrently with
// the test goroutine polling it for the write to land (see captureLogger's
// callers below), and a plain bytes.Buffer is not safe for that.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogger installs a slog logger writing to buf for the duration of
// the test and restores the previous default logger afterward.
func captureLogger(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestGo_RecoversPanicAndLogs(t *testing.T) {
	buf := captureLogger(t)
	done := make(chan struct{})
	Go("test-task", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not run")
	}
	// close(done) is deferred inside fn, so it fires during the panic unwind
	// before Recover's own deferred func runs and writes the log line; poll
	// briefly rather than assuming the write already landed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Len() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "panic in background task") {
		t.Fatalf("expected panic log, got: %q", out)
	}
	if !strings.Contains(out, "test-task") {
		t.Fatalf("expected task name in log, got: %q", out)
	}
	if !strings.Contains(out, "boom") {
		t.Fatalf("expected panic value in log, got: %q", out)
	}
	if !strings.Contains(out, "goroutine") {
		t.Fatalf("expected a stack trace in log, got: %q", out)
	}
}

func TestGo_NoPanicRunsNormally(t *testing.T) {
	var ran bool
	var mu sync.Mutex
	done := make(chan struct{})
	Go("normal-task", func() {
		mu.Lock()
		ran = true
		mu.Unlock()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if !ran {
		t.Fatal("expected fn to run")
	}
}

func TestRecover_FatalPanicPropagates(t *testing.T) {
	// A Fatal panic must propagate out of Recover's deferred func instead of
	// being swallowed, so a background task that deliberately raises Fatal
	// still crashes its goroutine root exactly like an unrecovered panic
	// would. Drive Recover the way a caller defers it, then catch the
	// re-panic one frame up to assert it is the original Fatal value.
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		func() {
			defer Recover("fatal-task")()
			panic(Fatal{Value: "fatal boom"})
		}()
	}()
	f, ok := recovered.(Fatal)
	if !ok {
		t.Fatalf("expected Fatal to propagate, got %#v", recovered)
	}
	if f.Value != "fatal boom" {
		t.Fatalf("expected original panic value preserved, got %v", f.Value)
	}
}

func TestRepanicFatal_NonFatalIsNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic, got %v", r)
		}
	}()
	RepanicFatal("not fatal")
	RepanicFatal(nil)
}

func TestRepanicFatal_RepanicsFatal(t *testing.T) {
	defer func() {
		r := recover()
		f, ok := r.(Fatal)
		if !ok {
			t.Fatalf("expected Fatal panic, got %#v", r)
		}
		if f.Value != "boom" {
			t.Fatalf("expected preserved value, got %v", f.Value)
		}
	}()
	RepanicFatal(Fatal{Value: "boom"})
	t.Fatal("unreachable: RepanicFatal should have re-panicked")
}

func TestAfterFunc_RecoversPanicAndLogs(t *testing.T) {
	buf := captureLogger(t)
	done := make(chan struct{})
	timer := AfterFunc(10*time.Millisecond, "timer-task", func() {
		defer close(done)
		panic("timer boom")
	})
	defer timer.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timer callback did not run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if buf.Len() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, "panic in background task") || !strings.Contains(out, "timer-task") {
		t.Fatalf("expected panic log with task name, got: %q", out)
	}
}

func TestAfterFunc_NoPanicRunsNormally(t *testing.T) {
	done := make(chan struct{})
	timer := AfterFunc(10*time.Millisecond, "timer-normal", func() {
		close(done)
	})
	defer timer.Stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timer callback did not run")
	}
}
