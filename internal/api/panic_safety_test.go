package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/logstream"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/safego"
)

// lockedLogBuffer collects slog output written from background goroutines.
type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureBackgroundLogs(t *testing.T) *lockedLogBuffer {
	t.Helper()
	buf := &lockedLogBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func owedSeqs(t *testing.T, store *db.Store, slug string) []int64 {
	t.Helper()
	owed, err := store.ListOwedRedeploys()
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	for _, o := range owed {
		if o.Slug == slug {
			seqs = append(seqs, o.Seq)
		}
	}
	return seqs
}

// A relaunched settings redeploy runs on its own goroutine, and the deploy
// lock it takes panics when the cross-process fence cannot be acquired. That
// panic must be contained rather than crash the server: the in-flight marker
// is cleared during unwinding and the seq stays owed for the next owner.
func TestRelaunchOwedRedeploys_ContainsFenceFailurePanic(t *testing.T) {
	h := newOutcomeHarness(t, "owed-fence", true)
	seq := armRedeploySeq(t, h.store, "owed-fence")
	if got := owedSeqs(t, h.store, "owed-fence"); len(got) != 1 || got[0] != seq {
		t.Fatalf("owed seqs before relaunch = %v, want [%d]", got, seq)
	}
	// A regular file where the lock directory should be makes every lock-file
	// open fail with ENOTDIR, so acquireDeployLock panics.
	notDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.srv.appOperationLockDir = notDir
	logs := captureBackgroundLogs(t)

	h.srv.RelaunchOwedRedeploys()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), `task="settings redeploy"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no contained panic logged for the settings redeploy; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if h.srv.isRedeployInFlight("owed-fence") {
		t.Fatal("redeploy in-flight marker still set after the redeploy goroutine unwound")
	}
	if got := owedSeqs(t, h.store, "owed-fence"); len(got) != 1 || got[0] != seq {
		t.Fatalf("owed seqs after the contained panic = %v, want [%d] still owed", got, seq)
	}
	if n := len(h.runs()); n != 0 {
		t.Fatalf("pool starts = %d, want 0 (nothing may run without the fence)", n)
	}
}

func recoverFrom(fn func()) (recovered any) {
	defer func() { recovered = recover() }()
	fn()
	return nil
}

// cycleRedeploy turns an ordinary panic into a failed outcome, but a
// safego.Fatal raised underneath it (Manager.Start's launch-to-publish window)
// must keep unwinding so the process exits instead of leaving an untracked
// launched process behind.
func TestRedeployApp_OrdinaryPanicFailsAndFatalPropagates(t *testing.T) {
	t.Run("ordinary", func(t *testing.T) {
		h := newOutcomeHarness(t, "cycle-panic", true)
		seq := armRedeploySeq(t, h.store, "cycle-panic")
		h.setResult(func(deploy.Params) (*deploy.PoolResult, error) { panic("boom") })
		if r := recoverFrom(func() { h.redeploy(seq) }); r != nil {
			t.Fatalf("ordinary panic escaped redeployApp: %v", r)
		}
		wantOutcome(t, h.lastRedeploy(), seq, db.RedeployFailed, "internal error: boom")
	})
	t.Run("fatal", func(t *testing.T) {
		h := newOutcomeHarness(t, "cycle-fatal", true)
		seq := armRedeploySeq(t, h.store, "cycle-fatal")
		h.setResult(func(deploy.Params) (*deploy.PoolResult, error) {
			panic(safego.Fatal{Value: "cycle-fatal"})
		})
		r := recoverFrom(func() { h.redeploy(seq) })
		if _, ok := r.(safego.Fatal); !ok {
			t.Fatalf("recovered %v (%T), want the safego.Fatal to propagate out of redeployApp", r, r)
		}
		if h.srv.isRedeployInFlight("cycle-fatal") {
			t.Fatal("in-flight marker still set after the Fatal unwound redeployApp")
		}
	})
}

func TestRedeployResize_FatalPropagates(t *testing.T) {
	s, app := newScaleTestServer(t, "resize-fatal", 2, &config.Config{})
	s.proxy.SetPoolSize(app.Slug, 1)
	s.deployReplica = func(deploy.Params, int) (*deploy.Result, error) {
		panic(safego.Fatal{Value: "resize-fatal"})
	}
	if err := s.store.DeleteReplica(app.ID, 1); err != nil {
		t.Fatal(err)
	}
	seq := armResizeSeq(t, s.store, app.Slug)
	s.markRedeployInFlight(app.Slug)
	r := recoverFrom(func() { s.redeployApp(app.Slug, seq) })
	if _, ok := r.(safego.Fatal); !ok {
		t.Fatalf("recovered %v (%T), want the safego.Fatal to propagate out of the resize", r, r)
	}
}

type panickingLogReader struct {
	mu    sync.Mutex
	calls int
}

func (r *panickingLogReader) Read(_ context.Context, _ process.ExternalLogs, cursor string, _ int32) (process.ExternalLogPage, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if cursor == "panic" {
		panic("provider sdk fault")
	}
	return process.ExternalLogPage{NextCursor: "next"}, nil
}

// A panicking provider reader must fail its own read promptly and release
// everything the read holds: the concurrency slot, the inflight entry and the
// done channel coalesced waiters block on.
func TestProviderLogRead_PanicFailsReadAndReleasesSlot(t *testing.T) {
	reader := &panickingLogReader{}
	c := &providerLogCoordinator{
		reader: reader, slots: make(chan struct{}, 1),
		inflight: make(map[providerLogReadKey]*providerLogCall),
		recent:   make(map[providerLogReadKey]providerLogCacheEntry),
	}
	details := process.ExternalLogs{Provider: "test", LogGroup: "g", LogStream: "s"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := c.read(ctx, details, "panic", 10)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("panicking read err = %v, want a prompt panic error", err)
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("panicking read err = %v, want it to name the panic", err)
	}
	c.mu.Lock()
	inflight := len(c.inflight)
	cached := len(c.recent)
	c.mu.Unlock()
	if inflight != 0 || cached != 0 {
		t.Fatalf("inflight=%d cached=%d after a panicked read, want 0 and 0", inflight, cached)
	}

	page, _, err := c.read(ctx, details, "ok", 10)
	if err != nil || page.NextCursor != "next" {
		t.Fatalf("read after the panic = %+v, %v; want success (the only slot must be free)", page, err)
	}
}

type panicFollowReader struct{}

func (panicFollowReader) ReadAll() ([]byte, error) { return nil, nil }
func (panicFollowReader) SnapshotTail(int) ([]logstream.Record, int64, error) {
	return nil, 0, nil
}
func (panicFollowReader) FollowFrom(context.Context, int64, chan<- logstream.Record) {
	panic("follow fault")
}
func (panicFollowReader) Tail(int) ([]string, error)            { return nil, nil }
func (panicFollowReader) Follow(context.Context, chan<- string) {}

// A panic in the follower must end the SSE response, so the client reconnects,
// rather than crash the server or leave a stream that only emits heartbeats.
func TestStreamLogReader_FollowPanicEndsStream(t *testing.T) {
	req := httptest.NewRequest("GET", "/logs", nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		streamLogReader(rec, req, panicFollowReader{}, 0, true)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream still open 5s after its follower panicked")
	}
}

// The PATCH handler launches its settings redeploy from a deferred func, which
// a behavioral test cannot reach with a broken fence without the handler's own
// lock acquisition failing first. Pin the launch to the panic-safe root.
func TestPatchApp_LaunchesRedeployUnderSafego(t *testing.T) {
	src, err := os.ReadFile("apps.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if strings.Contains(s, "go s.redeployApp(") {
		t.Fatal("apps.go launches redeployApp on a bare goroutine; use safego.Go")
	}
	want := `safego.Go("settings redeploy", func() { s.redeployApp(slug, seq) })`
	if strings.Count(s, want) != 1 {
		t.Fatalf("apps.go must launch the PATCH redeploy with %s exactly once", want)
	}
	for _, needle := range []string{
		`safego.Go("generation ledger cleanup"`,
		`safego.Go("generation retirement"`,
	} {
		if !strings.Contains(s, needle) {
			t.Fatalf("apps.go missing %s", needle)
		}
	}
	sched, err := os.ReadFile("schedules.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sched), "go s.cancelWhenRunDone(") {
		t.Fatal("schedules.go launches cancelWhenRunDone on a bare goroutine; use safego.Go")
	}
}
