package cli

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rvben/shinyhub/internal/localrun"
)

func TestDevSmallScreensKeepStatusAndOutput(t *testing.T) {
	m := testDevModel()
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1})
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Attempt: 1, Message: "app output", At: time.Now()})
	for _, size := range [][2]int{{24, 8}, {32, 10}, {40, 12}, {80, 8}, {100, 10}} {
		m.width, m.height = size[0], size[1]
		view := m.View().Content
		for _, want := range []string{"sales", "Serving v1", "app output", "q quit"} {
			if !strings.Contains(view, want) {
				t.Fatalf("missing %q at %v: %s", want, size, view)
			}
		}
		if strings.Contains(view, "Enlarge") {
			t.Fatal("small screen was refused")
		}
	}
	m.width, m.height = 24, 8
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "failed", Attempt: 2, Message: "SyntaxError"})
	if view := m.View().Content; !strings.Contains(view, "Serving v1 · FAILED") || !strings.Contains(view, "SyntaxError") {
		t.Fatalf("failed change hid serving status or diagnostic: %s", view)
	}
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "stopped"})
	if view := m.View().Content; !strings.Contains(view, "Stopped · u resume") {
		t.Fatalf("stopped recovery hidden: %s", view)
	}
}

func TestDevAllScreenSizesStayWithinBounds(t *testing.T) {
	m := testDevModel()
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "ready", Attempt: 1, Generation: 1})
	m.apply(localrun.Event{Type: "phase", App: "sales", Phase: "failed", Attempt: 2, Message: strings.Repeat("bad source ", 20)})
	m.apply(localrun.Event{Type: "log", App: "sales", Source: "app", Attempt: 2, Message: "\x1b[2J正常 " + strings.Repeat("long log ", 20)})
	for _, width := range []int{1, 6, 12, 20, 24, 32, 40, 41, 42, 80, 100} {
		for _, height := range []int{1, 2, 3, 4, 6, 8, 10, 13, 14, 24} {
			m.width, m.height = width, height
			for _, scope := range []int{devAllApps, devShinyHub, 0, 1} {
				m.selected = scope
				for _, mode := range []string{"logs", "help", "resources", "filter", "action", "ended"} {
					m.help, m.showResources, m.searching, m.ended = mode == "help", mode == "resources", mode == "filter", mode == "ended"
					m.actionError = ""
					if mode == "action" {
						m.actionError = "Select an app to control it"
					}
					view := m.View().Content
					lines := strings.Split(view, "\n")
					if len(lines) > height {
						t.Fatalf("height overflow at %dx%d %s scope%d: %s", width, height, mode, scope, view)
					}
					for _, line := range lines {
						if ansi.StringWidth(line) > width {
							t.Fatalf("width overflow at %dx%d %s: %s", width, height, mode, line)
						}
					}
					if strings.Contains(view, "\x1b") {
						t.Fatal("untrusted terminal sequences escaped sanitizer")
					}
					if width >= 20 && mode != "filter" && !strings.Contains(view, "q quit") {
						t.Fatalf("quit hidden at %dx%d %s: %s", width, height, mode, view)
					}
				}
			}
		}
	}
}

func TestDevCompactHelpScrollKeepsLogsAndResourcesAnchored(t *testing.T) {
	m := testDevModel()
	m.width, m.height = 24, 8
	m.following, m.offset, m.resourceOffset = false, 7, 3
	m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if !strings.Contains(m.View().Content, "r restart") {
		t.Fatal("help did not explain controls")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	end := m.View().Content
	if !strings.Contains(end, "stop all apps") {
		t.Fatalf("End did not reveal final help: %s", end)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.View().Content == end || m.following || m.offset != 7 || m.resourceOffset != 3 {
		t.Fatal("help scroll was stuck or moved hidden logs/resources")
	}
	for _, key := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: 'f', Text: "f"}, {Code: tea.KeySpace}} {
		m.Update(key)
	}
	if m.following || m.offset != 7 {
		t.Fatal("hidden log controls reset saved viewport")
	}
	m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if m.help || !m.searching {
		t.Fatal("filter did not replace compact help")
	}
}

func TestDevCompactResourceEndAndResize(t *testing.T) {
	m := testDevModel()
	m.width, m.height, m.showResources = 24, 8, true
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	if !strings.Contains(m.View().Content, "2s.") {
		t.Fatalf("resource End missed final wrapped row: %s", m.View().Content)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 28})
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 6})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	bottom := m.View().Content
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.View().Content == bottom {
		t.Fatal("resource scroll stuck after crossing layout modes")
	}
	_, _, _, _, viewport := m.paneLayout(m.logWidth(), m.height)
	before := m.resourceOffset
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if before-m.resourceOffset > viewport {
		t.Fatal("resource paging skipped rows in a short split")
	}
	m.showResources = false
	m.offset = 0
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.offset > viewport {
		t.Fatal("log paging skipped rows in a short split")
	}
}
