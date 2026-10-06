package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Compact terminals trade metadata and decoration for status and usable output.
// Both axes matter: a wide but short split needs the same vertical economy.
func (m *devModel) compact() bool { return m.width < 42 || m.height < 14 }
func (m *devModel) compactPadding() int {
	if m.width >= 20 {
		return 1
	}
	return 0
}

func (m *devModel) compactStatus() (string, string) {
	s := m.style
	if m.selected < 0 {
		name := "All apps"
		if m.selected == devShinyHub {
			name = "ShinyHub"
		}
		healthy, failed, suspended := 0, 0, 0
		for _, app := range m.apps {
			if app.generation > 0 && !app.stopped {
				healthy++
			}
			if app.failure != "" {
				failed++
			}
			if app.suspended {
				suspended++
			}
		}
		status := fmt.Sprintf("%d/%d serving", healthy, len(m.apps))
		if failed > 0 {
			status += s.red(fmt.Sprintf(" · %d failed", failed))
		} else if suspended > 0 {
			status += s.dim(fmt.Sprintf(" · %d stopped", suspended))
		}
		return name, status
	}
	app := m.apps[m.selected]
	name := devSafeText(app.slug)
	status := "Starting"
	switch {
	case app.suspended:
		status = s.dim("Stopped") + " · u resume"
	case app.generation > 0 && !app.stopped:
		status = s.green(fmt.Sprintf("Serving v%d", app.generation))
		if app.failure != "" {
			status += " · " + s.red("FAILED")
		} else if app.phase != "ready" {
			status += " · " + s.yellow("starting")
		}
	case app.failure != "":
		status = "Not serving · " + s.red("FAILED")
	case app.phase == "stopping":
		status = s.dim("Stopping")
	case app.phase == "resuming":
		status = s.yellow("Resuming")
	case app.phase == "preparing":
		status = s.yellow("Preparing")
	}
	return name, status
}

func (m *devModel) compactPaneLayout(width, height int) ([]string, string, []string, int, int) {
	name, status := m.compactStatus()
	bodyHeight := max(0, height-2) // one header and one footer; no decorative rows
	var pane []string
	if bodyHeight >= 2 {
		pane = append(pane, status)
	}
	// Failure details take precedence over tab metadata in a short split.
	if m.selected >= 0 && !m.help && !m.showResources && m.apps[m.selected].failure != "" && bodyHeight >= 4 {
		pane = append(pane, m.style.red(devSafeText(m.apps[m.selected].failure)))
	}
	if m.actionError != "" && bodyHeight >= 3 {
		pane = append(pane, m.style.red(m.actionError))
	}
	context := "Runtime"
	if m.latest {
		context = "Latest"
	}
	if m.selected < 0 {
		context = "Logs"
	}
	if m.showResources {
		context = "Resources"
	}
	if m.help {
		context = "Help"
	}
	if m.filter != "" || m.searching {
		context = "Filter: " + m.filter
	}
	if !m.following && !m.showResources && !m.help {
		context += " · PAUSED"
	}
	if len(pane)+1 < bodyHeight {
		pane = append(pane, devAccent(m.style, "["+context+"]"))
	}
	if bodyHeight < 2 {
		name += " · " + status
	}
	return pane, devStrong(m.style, name), nil, bodyHeight, max(0, bodyHeight-len(pane))
}

func (m *devModel) compactHelpLines(width int) []string {
	entries := []string{
		"r restart · keeps healthy app", "x stop · suspends reloads", "u resume · latest source",
		"↑/↓ switch view", "a all apps", "s ShinyHub logs", "m resources / logs", "Tab runtime / latest",
		"/ filter logs", "o open app", "PgUp/PgDn scroll", "Home/End first / last", "f follow logs", "Space pause / follow",
		"Esc clear filter / close help", "? close help", "q quit · stop all apps",
	}
	if m.selected >= 0 && m.apps[m.selected].url != "" {
		entries = append(entries, "URL "+m.apps[m.selected].url)
	}
	var rows []string
	for _, entry := range entries {
		rows = append(rows, strings.Split(ansi.Hardwrap(devFooter(m.style, entry), width, true), "\n")...)
	}
	return rows
}

func (m *devModel) compactFooter(width int) string {
	candidates := []string{
		"↑/↓ views  r restart  x stop  u resume  ? help  q quit",
		"↑/↓ views  ? help  q quit", "↑↓ ? help q quit", "↑↓ ? q quit", "? q quit", "q quit", "q",
	}
	if m.help {
		candidates = []string{"PgUp/PgDn scroll  ? back  q quit", "? back  q quit", "? q quit", "q quit", "q"}
	}
	if m.searching {
		candidates = []string{"Enter apply  Esc clear  ^C quit", "Esc clear  ^C quit", "Esc  ^C quit", "^C quit", "^C"}
	}
	if m.ended {
		candidates = []string{"Session ended · q quit", "Ended · q quit", "q quit", "q"}
	}
	for _, text := range candidates {
		if ansi.StringWidth(text) <= width {
			return devFooter(m.style, text)
		}
	}
	return devFit(candidates[len(candidates)-1], width)
}

func (m *devModel) compactView() tea.View {
	view := tea.NewView("")
	view.AltScreen = true
	if m.width <= 0 || m.height <= 0 {
		return view
	}
	width := m.logWidth()
	padding := strings.Repeat(" ", m.compactPadding())
	pane, title, _, bodyHeight, viewport := m.paneLayout(width, m.height)
	if m.height == 1 {
		view.Content = padding + devFit(m.compactFooter(width), width)
		return view
	}
	var content []string
	offset := 0
	switch {
	case m.help:
		content = m.compactHelpLines(width)
		offset = min(m.helpOffset, max(0, len(content)-viewport))
	case m.showResources:
		content = m.resourceDisplayLines(width)
		offset = min(m.resourceOffset, max(0, len(content)-viewport))
	default:
		content = m.logLines(width)
		if len(content) == 0 {
			content = []string{m.style.dim("Waiting for output")}
			if m.filter != "" {
				content = []string{m.style.dim("No matching logs")}
			}
		}
		offset = max(0, len(content)-viewport)
		if !m.following {
			offset -= min(m.offset, offset)
		}
	}
	end := min(len(content), offset+viewport)
	pane = append(pane, content[offset:end]...)
	for len(pane) < bodyHeight {
		pane = append(pane, "")
	}
	lines := []string{padding + devFit(title, width)}
	for _, row := range pane {
		lines = append(lines, padding+devFit(row, width))
	}
	lines = append(lines, padding+devFit(m.compactFooter(width), width))
	view.Content = strings.Join(lines, "\n")
	return view
}
