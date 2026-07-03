package proxy

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// seedServedAt back-dates a slug's first-served time so the wsWarnGrace window is
// already exceeded without the test sleeping.
func seedServedAt(p *Proxy, slug string, ago time.Duration) {
	p.seenMu.Lock()
	p.firstServedAt[slug] = time.Now().Add(-ago)
	p.seenMu.Unlock()
}

func TestConnectivityHealth_States(t *testing.T) {
	p := New()

	// Never served: nothing to report.
	if ever, serving := p.ConnectivityHealth("app"); ever || serving {
		t.Fatalf("never-served: got (ever=%v, serving=%v), want (false,false)", ever, serving)
	}

	// Served, still inside the grace window: not yet a warning.
	p.RecordActivity("app")
	if ever, serving := p.ConnectivityHealth("app"); ever || serving {
		t.Fatalf("within grace: got (ever=%v, serving=%v), want (false,false)", ever, serving)
	}

	// Served longer than the grace window with no WebSocket: warn.
	seedServedAt(p, "app", 2*wsWarnGrace)
	if ever, serving := p.ConnectivityHealth("app"); ever || !serving {
		t.Fatalf("past grace, no WS: got (ever=%v, serving=%v), want (false,true)", ever, serving)
	}

	// A completed WebSocket clears the warning and reports healthy.
	p.MarkWSReady("app")
	if ever, serving := p.ConnectivityHealth("app"); !ever || serving {
		t.Fatalf("after WS: got (ever=%v, serving=%v), want (true,false)", ever, serving)
	}
}

func TestConnectivityHealth_ResetsOnClear(t *testing.T) {
	p := New()
	seedServedAt(p, "app", 2*wsWarnGrace)
	if _, serving := p.ConnectivityHealth("app"); !serving {
		t.Fatalf("precondition: expected serving-without-ws before clear")
	}
	// Deregister/hibernate clears WS readiness AND the detection window, so a
	// re-woken pool starts fresh rather than warning immediately.
	p.clearWSReady("app")
	if ever, serving := p.ConnectivityHealth("app"); ever || serving {
		t.Fatalf("after clear: got (ever=%v, serving=%v), want (false,false)", ever, serving)
	}
}

func TestConnectivityHealth_WSReadySuppressesWarning(t *testing.T) {
	p := New()
	// Once a WebSocket has connected, further HTTP traffic must never re-arm the
	// warning for this lifecycle (RecordActivity skips firstServedAt when ready).
	p.MarkWSReady("app")
	seedServedAt(p, "app", 2*wsWarnGrace) // even if a stale served-at leaks in
	p.RecordActivity("app")
	if ever, serving := p.ConnectivityHealth("app"); !ever || serving {
		t.Fatalf("ws-ready then served: got (ever=%v, serving=%v), want (true,false)", ever, serving)
	}
}

func TestRecordActivity_LogsOnceWhenServingWithoutWS(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(prev)

	p := New()
	// Back-date first-served so the very next RecordActivity trips the warning.
	seedServedAt(p, "app", 2*wsWarnGrace)
	p.RecordActivity("app")
	p.RecordActivity("app") // must NOT log a second time

	got := strings.Count(buf.String(), "no WebSocket has connected")
	if got != 1 {
		t.Fatalf("serving-without-ws ERROR logged %d times, want exactly 1\nlog:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "docs/reverse-proxy/caddy.md") {
		t.Errorf("log message should point at the reverse-proxy docs; got:\n%s", buf.String())
	}
}
