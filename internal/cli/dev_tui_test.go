package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rvben/shinyhub/internal/localrun"
)

func testDevModel() *devModel {
	return newDevModel([]string{"sales", "ops"}, nil, nil, map[string]chan struct{}{"sales": make(chan struct{}, 1), "ops": make(chan struct{}, 1)}, func() {}, styler{})
}
func TestDevViewKeepsServingSeparateFromFailedSave(t *testing.T) {
	m := testDevModel()
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1, URL: "http://localhost/app/sales/", At: time.Now()})
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "reloading", Attempt: 2})
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Stream: "stderr", Attempt: 2, Message: "SyntaxError: expected ':'"})
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "failed", Attempt: 2, Message: "app exited during startup (exit 1)"})
	view := m.View().Content
	for _, want := range []string{"Serving v1", "Latest save failed", "SyntaxError", "r to retry", "sales", "ops"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 3, Generation: 2, URL: "http://localhost/app/sales/", At: time.Now()})
	if strings.Contains(m.View().Content, "Latest save failed") || m.apps[0].failure != "" {
		t.Fatal("recovery left failure visible")
	}
}
func TestDevControlsRetrySelectedAppAndPauseLogFollowing(t *testing.T) {
	m := testDevModel()
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if len(m.retries["ops"]) != 1 || len(m.retries["sales"]) != 0 {
		t.Fatal("retry targeted wrong app")
	}
	m.apply(localrun.Event{Type: "phase", App: "ops", Phase: "ready", Attempt: 1, Generation: 1})
	for i := 0; i < 40; i++ {
		m.apply(localrun.Event{Type: "log", App: "ops", Source: "app", Attempt: 1, Message: "log line"})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.following || m.offset == 0 {
		t.Fatal("scroll did not pause following")
	}
	before := m.offset
	m.apply(localrun.Event{Type: "log", App: "ops", Source: "app", Attempt: 1, Message: "new line"})
	if m.offset <= before {
		t.Fatal("paused viewport moved with logs")
	}
	m.Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if !m.following || m.offset != 0 {
		t.Fatal("follow did not return to tail")
	}
}
func TestDevLayoutBoundsAndTerminalInjection(t *testing.T) {
	m := testDevModel()
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1})
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Attempt: 1, Message: "\x1b[2J\x1b]0;evil\x07正常 " + strings.Repeat("long log ", 100)})
	for _, size := range [][2]int{{42, 14}, {80, 24}, {100, 28}, {160, 40}} {
		m.width, m.height = size[0], size[1]
		if size[1] == 14 {
			m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "failed", Attempt: 2, Message: "SyntaxError: failed candidate"})
		}
		view := m.View().Content
		if strings.Contains(view, "\x1b") {
			t.Fatal("child output escaped the terminal sanitizer")
		}
		lines := strings.Split(view, "\n")
		if len(lines) > m.height {
			t.Fatalf("height %d > %d", len(lines), m.height)
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > m.width {
				t.Fatalf("width %d > %d: %s", ansi.StringWidth(line), m.width, line)
			}
		}
	}
}
func TestDevBoundedLogsAndSearch(t *testing.T) {
	m := testDevModel()
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1})
	for i := 0; i < devLogLimit+20; i++ {
		m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Attempt: 1, Message: "noise"})
	}
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Attempt: 1, Message: "needle"})
	if len(m.apps[0].logs) != devLogLimit {
		t.Fatal("log retention is unbounded")
	}
	m.filter = "NEEDLE"
	if got := m.logLines(80); len(got) != 1 || !strings.Contains(got[0], "needle") {
		t.Fatalf("filter = %v", got)
	}
}
func TestDevQuitCancelsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := testDevModel()
	m.cancel = cancel
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if ctx.Err() == nil || cmd == nil {
		t.Fatal("quit did not cancel the app session")
	}
}

func TestDevSessionViewsRouteLogsAndKeepAppActionsScoped(t *testing.T) {
	m := testDevModel()
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1})
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Attempt: 1, Message: "sales runtime"})
	m.apply(localrun.Event{Type: "log", App: "ops", Source: "app", Attempt: 2, Message: "ops startup"})
	m.apply(localrun.Event{Type: "log", App: "ops", Source: "reload", Attempt: 2, Message: "installing dependencies"})
	m.apply(localrun.Event{Type: "phase", App: "ops", Phase: "failed", Attempt: 2, Message: "candidate failed"})
	m.apply(localrun.Event{Type: "log", Source: "reload", Message: "fleet warning"})
	m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	logs := strings.Join(m.logLines(120), "\n")
	for _, want := range []string{"[sales #1]", "sales runtime", "[ops #2]", "ops startup", "installing dependencies", "failed", "[session]", "fleet warning"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("all apps missing %q: %s", want, logs)
		}
	}
	m.filter = "OPS"
	if got := strings.Join(m.logLines(120), "\n"); strings.Contains(got, "sales runtime") || !strings.Contains(got, "ops startup") {
		t.Fatalf("app identity filter: %s", got)
	}
	m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	logs = strings.Join(m.logLines(120), "\n")
	if strings.Contains(logs, "sales runtime") || strings.Contains(logs, "ops startup") {
		t.Fatalf("app output leaked into hub logs: %s", logs)
	}
	for _, want := range []string{"installing dependencies", "candidate failed", "fleet warning"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("hub missing %q: %s", want, logs)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m.Update(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if len(m.retries["sales"]) != 0 || len(m.retries["ops"]) != 0 {
		t.Fatal("session view retried an app")
	}
	if !strings.Contains(m.actionError, "Select an app") {
		t.Fatal("session action lacked guidance")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.selected != 0 || m.latest {
		t.Fatal("healthy app selection did not show runtime")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.selected != 1 || !m.latest {
		t.Fatal("failed app selection did not show latest change")
	}
}

func TestDevSessionViewsBoundedPausedAndResponsive(t *testing.T) {
	m := testDevModel()
	m.selected = devAllApps
	for i := 0; i < 40; i++ {
		m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Message: "runtime"})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	before := m.offset
	m.apply(localrun.Event{Type: "log", App: "ops", Source: "app", Message: "runtime"})
	if m.offset != before+1 {
		t.Fatal("aggregate paused viewport moved")
	}
	m.selected = devShinyHub
	before = m.offset
	m.apply(localrun.Event{Type: "log", App: "ops", Source: "app", Message: "hidden output"})
	if m.offset != before {
		t.Fatal("hidden runtime shifted hub viewport")
	}
	m.apply(localrun.Event{Type: "phase", App: "ops", Phase: "reloading", Message: "new save"})
	if m.offset != before+1 {
		t.Fatal("phase did not anchor hub viewport")
	}
	m.following = true
	for i := 0; i < devLogLimit+20; i++ {
		m.apply(localrun.Event{Type: "log", Source: "reload", Message: strings.Repeat("x", 400)})
	}
	for _, b := range []devLogBuffer{m.sessionLogs, m.hubLogs} {
		if len(b.logs) > devLogLimit || b.bytes > 512*1024 {
			t.Fatal("session retention unbounded")
		}
	}
	for _, scope := range []int{devAllApps, devShinyHub, 0, 1} {
		m.selected = scope
		for _, size := range [][2]int{{42, 14}, {80, 24}, {100, 28}} {
			m.width, m.height = size[0], size[1]
			view := m.View().Content
			if len(strings.Split(view, "\n")) > m.height {
				t.Fatal("session view exceeds terminal height")
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > m.width {
					t.Fatal("session view exceeds terminal width")
				}
			}
			if !strings.Contains(view, "q stop") {
				t.Fatal("stop control missing")
			}
		}
	}
}

func TestDevSessionDiagnosticsHaveNoFalseAppAttribution(t *testing.T) {
	var events []localrun.Event
	w := &devDiagnosticWriter{emit: func(e localrun.Event) { events = append(events, e) }}
	_, _ = w.Write([]byte("fleet warning\n" + strings.Repeat("x", 20000)))
	w.flush()
	var bytes int
	for _, e := range events {
		if e.App != "" || e.At.IsZero() || len(e.Message) > 8192 {
			t.Fatalf("invalid session diagnostic: %+v", e)
		}
		bytes += len(e.Message)
	}
	if bytes != len("fleet warning")+20000 || w.fragment != "" {
		t.Fatal("diagnostic writer lost partial output")
	}
}

func TestDevPausedRetention(t *testing.T) {
	m := testDevModel()
	m.selected = devAllApps
	m.width = 100
	m.height = 28
	for i := 0; i < devLogLimit; i++ {
		m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Message: strings.Repeat("x", i%7) + string(rune(0x4e00+i))})
	}
	m.following = false
	m.offset = 20
	before := m.View().Content
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Message: "newest"})
	if after := m.View().Content; before != after {
		t.Fatalf("PAUSED text changed on append at retention limit")
	}
}
func TestDevResourceEnd(t *testing.T) {
	m := testDevModel()
	m.width = 42
	m.height = 14
	m.showResources = true
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	view := m.View().Content
	if !strings.Contains(view, "every 2s.") {
		t.Fatalf("End did not reveal last detail line: %s", view)
	}
}
func TestDevResourceHiddenTab(t *testing.T) {
	m := testDevModel()
	m.showResources = true
	m.following = false
	m.offset = 20
	m.filter = "needle"
	latest := m.latest
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.latest != latest || m.following || m.offset != 20 {
		t.Fatal("Tab in Resources changes hidden log tab and resets saved scroll")
	}
}

func TestDevFailureDrainsFinalDiagnosticsAndKeepsMetricsLive(t *testing.T) {
	m := testDevModel()
	events := make(chan localrun.Event, 4)
	m.events = events
	m.apply(localrun.Event{Type: "process", App: "sales", Phase: "started", Attempt: 1, PID: 100})
	events <- localrun.Event{Type: "log", App: "sales", Source: "app", Stream: "stderr", Attempt: 1, Message: "SyntaxError: final traceback"}
	events <- localrun.Event{Type: "process", App: "sales", Phase: "exited", Attempt: 1, PID: 100}
	events <- localrun.Event{Type: "phase", App: "sales", Phase: "stopped", Attempt: 1, Message: "startup failed"}
	_, cmd := m.Update(devDoneMsg{errors.New("startup failed")})
	if cmd == nil || len(events) != 0 || len(m.apps[0].processes) != 0 || !m.apps[0].stopped {
		t.Fatal("failure left telemetry pending or frozen")
	}
	if !strings.Contains(m.View().Content, "final traceback") {
		t.Fatal("completion swallowed the final startup diagnostic")
	}
	updates := make(chan devResourceMsg, 1)
	m.resourceUpdates = updates
	updates <- devResourceMsg{at: time.Now(), hostCPU: func() *float64 { v := 42.0; return &v }()}
	msg := cmd()
	m.Update(msg)
	if m.resources.hostCPU == nil || *m.resources.hostCPU != 42 {
		t.Fatal("failure view did not consume fresh metrics")
	}
}

func TestDevResourceScrollClamped(t *testing.T) {
	m := testDevModel()
	m.width = 42
	m.height = 14
	m.showResources = true
	for i := 0; i < 30; i++ {
		m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	bottom := m.View().Content
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.View().Content == bottom {
		t.Fatal("PgUp remains stuck at bottom after repeated PgDown")
	}
}
func TestDevHomeWrappedLogs(t *testing.T) {
	m := testDevModel()
	m.width = 100
	m.height = 28
	m.selected = devAllApps
	for i := 0; i < 50; i++ {
		m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Message: strings.Repeat("x", 70)})
	}
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Message: "END"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	lines := m.logLines(m.logWidth())
	if m.offset < len(lines)-m.height {
		t.Fatalf("Home offset=%d too short for %d wrapped rows", m.offset, len(lines))
	}
}
