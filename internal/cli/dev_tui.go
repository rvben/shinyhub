package cli

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rvben/shinyhub/internal/localrun"
)

const devLogLimit = 2000

const (
	devAllApps  = -2
	devShinyHub = -1
)

// A single bounded session stream preserves receipt order across apps.
type devLogBuffer struct {
	logs  []localrun.Event
	bytes int
}

func (b *devLogBuffer) append(event localrun.Event) []localrun.Event {
	b.logs = append(b.logs, event)
	b.bytes += len(event.Message)
	remove := 0
	for len(b.logs)-remove > devLogLimit || b.bytes > 512*1024 {
		b.bytes -= len(b.logs[remove].Message)
		remove++
	}
	var removed []localrun.Event
	if remove > 0 {
		removed = append(removed, b.logs[:remove]...)
		copy(b.logs, b.logs[remove:])
		clear(b.logs[len(b.logs)-remove:])
		b.logs = b.logs[:len(b.logs)-remove]
	}
	return removed
}

type devAppView struct {
	slug, phase, activity, url, failure       string
	generation, servingAttempt, latestAttempt int64
	lastReady                                 time.Time
	logs                                      []localrun.Event
	logBytes                                  int
	stopped                                   bool
	suspended                                 bool
	processes                                 map[int64]int
}
type devDoneMsg struct{ err error }
type devActionError string

type devModel struct {
	apps                               []devAppView
	sessionLogs, hubLogs               devLogBuffer
	selected, width, height            int
	latest, following, searching, help bool
	filter                             string
	offset                             int
	style                              styler
	events                             <-chan localrun.Event
	done                               <-chan error
	finished                           chan error
	controls                           map[string]chan localrun.Control
	cancel                             context.CancelFunc
	ended                              bool
	actionError                        string
	resources                          devResourceMsg
	resourceUpdates                    <-chan devResourceMsg
	clock                              time.Time
	showResources                      bool
	resourceOffset                     int
}

func newDevModel(slugs []string, events <-chan localrun.Event, done <-chan error, controls map[string]chan localrun.Control, cancel context.CancelFunc, style styler) *devModel {
	m := &devModel{width: 100, height: 28, clock: time.Now(), latest: true, following: true, events: events, done: done, finished: make(chan error, 1), controls: controls, cancel: cancel, style: style}
	for _, slug := range slugs {
		m.apps = append(m.apps, devAppView{slug: slug, phase: "preparing", activity: "Preparing workspace"})
	}
	return m
}
func (m *devModel) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case metrics := <-m.resourceUpdates:
			return metrics
		case event := <-m.events:
			return event
		case err := <-m.done:
			return devDoneMsg{err}
		}
	}
}

type devClockMsg time.Time

func devClockTick() tea.Cmd {
	return tea.Tick(time.Second, func(at time.Time) tea.Msg { return devClockMsg(at) })
}
func (m *devModel) Init() tea.Cmd { return tea.Batch(m.listen(), devClockTick()) }
func (m *devModel) apply(event localrun.Event) {
	if event.Type == "process" {
		for i := range m.apps {
			app := &m.apps[i]
			if app.slug != event.App {
				continue
			}
			if app.processes == nil {
				app.processes = make(map[int64]int)
			}
			if event.Phase == "started" {
				app.processes[event.Attempt] = event.PID
			}
			if event.Phase == "exited" && app.processes[event.Attempt] == event.PID {
				delete(app.processes, event.Attempt)
			}
		}
		return
	}
	event.Message = devSafeText(event.Message)
	before, evictedRows := 0, 0
	var selectedApp devAppView
	if m.selected >= 0 {
		selectedApp = m.apps[m.selected]
	}
	countEvicted := func(events []localrun.Event) {
		if m.following {
			return
		}
		for _, e := range events {
			evictedRows += len(m.eventLogLines(e, selectedApp, m.logWidth()))
		}
	}
	if !m.following {
		before = len(m.logLines(m.logWidth()))
	}
	defer func() {
		if !m.following {
			m.offset += max(0, len(m.logLines(m.logWidth()))-before+evictedRows)
		}
	}()
	removed := m.sessionLogs.append(event)
	if m.selected == devAllApps {
		countEvicted(removed)
	}
	if event.Source != "app" {
		removed = m.hubLogs.append(event)
		if m.selected == devShinyHub {
			countEvicted(removed)
		}
	}
	for i := range m.apps {
		app := &m.apps[i]
		if app.slug != event.App {
			continue
		}
		if event.Type == "log" {
			event.Message = devSafeText(event.Message)
			app.logs = append(app.logs, event)
			app.logBytes += len(event.Message)
			remove := 0
			for len(app.logs)-remove > devLogLimit || app.logBytes > 512*1024 {
				app.logBytes -= len(app.logs[remove].Message)
				remove++
			}
			if remove > 0 {
				if i == m.selected {
					countEvicted(app.logs[:remove])
				}
				copy(app.logs, app.logs[remove:])
				clear(app.logs[len(app.logs)-remove:])
				app.logs = app.logs[:len(app.logs)-remove]
			}
			return
		}
		app.phase = event.Phase
		app.activity = devSafeText(event.Message)
		app.latestAttempt = event.Attempt
		switch event.Phase {
		case "ready":
			app.generation = event.Generation
			app.servingAttempt = event.Attempt
			app.url = event.URL
			app.failure = ""
			app.lastReady = event.At
			app.stopped = false
			app.suspended = false
			if i == m.selected {
				m.latest = false
				m.offset = 0
				m.following = true
			}
		case "failed":
			app.failure = devSafeText(event.Message)
			if i == m.selected {
				m.latest = true
				m.offset = 0
				m.following = true
			}
		case "stopped":
			app.stopped = true
			app.suspended = true
			app.failure = ""
		case "stopping":
			app.stopped = true
		case "resuming":
			app.suspended = false
		case "exited":
			app.stopped = true
			app.failure = devSafeText(event.Message)
		}
		return
	}
}
func (m *devModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case devClockMsg:
		m.clock = time.Time(msg)
		return m, devClockTick()
	case devResourceMsg:
		m.resources = msg
		m.clock = time.Now()
		return m, m.listen()
	case localrun.Event:
		m.apply(msg)
		return m, m.listen()
	case devDoneMsg:
		// The runner queues final logs and exit events before publishing done.
		// Drain those records if completion wins the select over the event queue.
	drain:
		for {
			select {
			case event := <-m.events:
				m.apply(event)
			default:
				break drain
			}
		}
		m.ended = true
		if msg.err == nil {
			return m, tea.Quit
		}
		m.actionError = devSafeText(msg.err.Error())
		for i := range m.apps {
			m.apps[i].stopped = true
			clear(m.apps[i].processes)
		}
		return m, m.listen() // Keep failure visible and host observations live until q.
	case devActionError:
		m.actionError = string(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" || (!m.searching && key == "q") {
			m.cancel()
			return m, tea.Quit
		}
		if m.searching {
			switch key {
			case "esc":
				m.searching = false
				m.filter = ""
			case "enter":
				m.searching = false
			case "backspace":
				r := []rune(m.filter)
				if len(r) > 0 {
					m.filter = string(r[:len(r)-1])
				}
			default:
				if msg.Text != "" {
					m.filter += devSafeText(msg.Text)
				}
			}
			m.offset = 0
			m.following = true
			return m, nil
		}
		if m.showResources {
			_, _, _, _, viewport := m.paneLayout(m.logWidth(), m.height)
			maxOffset := max(0, len(m.resourceDisplayLines(m.logWidth()))-viewport)
			m.resourceOffset = min(m.resourceOffset, maxOffset)
			switch key {
			case "pgup":
				m.resourceOffset = max(0, m.resourceOffset-max(1, m.height/2))
				return m, nil
			case "pgdown":
				m.resourceOffset = min(maxOffset, m.resourceOffset+max(1, m.height/2))
				return m, nil
			case "home":
				m.resourceOffset = 0
				return m, nil
			case "end":
				m.resourceOffset = maxOffset
				return m, nil
			}
		}
		switch key {
		case "m":
			m.showResources = !m.showResources
			m.resourceOffset = 0
		case "up", "k", "left":
			if m.selected > devAllApps {
				m.selected--
				m.resetLogs()
			}
		case "down", "j", "right":
			if m.selected+1 < len(m.apps) {
				m.selected++
				m.resetLogs()
			}
		case "a":
			m.selected = devAllApps
			m.resetLogs()
		case "s":
			m.selected = devShinyHub
			m.resetLogs()
		case "tab":
			if m.showResources || m.selected < 0 {
				break
			}
			m.latest = !m.latest
			m.offset = 0
			m.following = true
		case "pgup":
			m.following = false
			m.offset += max(1, m.height/2)
		case "pgdown":
			m.offset = max(0, m.offset-max(1, m.height/2))
			m.following = m.offset == 0
		case "home":
			m.following = false
			m.offset = len(m.logLines(m.logWidth()))
		case "end", "f":
			if m.showResources {
				break
			}
			m.following = true
			m.offset = 0
		case "space":
			if m.showResources {
				break
			}
			m.following = !m.following
		case "/":
			m.showResources = false
			m.searching = true
		case "esc":
			m.filter = ""
			m.help = false
			m.actionError = ""
		case "?":
			m.help = !m.help
		case "o":
			if m.selected < 0 {
				m.actionError = "Select an app with ↑/↓ to open its URL"
				break
			}
			if url := m.apps[m.selected].url; url != "" && !m.apps[m.selected].stopped {
				return m, func() tea.Msg {
					if err := openBrowserURL(url); err != nil {
						return devActionError("Could not open browser; copy the URL above")
					}
					return nil
				}
			}
		case "r", "x", "u":
			if m.selected < 0 {
				m.actionError = "Select an app with ↑/↓ to control it"
				break
			}
			app := m.apps[m.selected]
			control := map[string]localrun.Control{"r": localrun.Restart, "x": localrun.Stop, "u": localrun.Resume}[key]
			if key == "r" && app.suspended {
				m.actionError = "App is stopped; press u to resume the latest source"
				break
			}
			if key == "u" && !app.suspended {
				m.actionError = "App is not stopped; press r to restart"
				break
			}
			if !m.ended {
				select {
				case m.controls[app.slug] <- control:
					m.actionError = ""
				default:
					m.actionError = "App controls are busy; try again shortly"
				}
			} else {
				m.actionError = "Session ended; start shinyhub dev again"
			}
		}
	}
	return m, nil
}
func (m *devModel) resetLogs() {
	if m.selected >= 0 {
		app := m.apps[m.selected]
		m.latest = app.failure != "" || app.generation == 0
	}
	m.offset = 0
	m.following = true
	m.filter = ""
	m.actionError = ""
	m.resourceOffset = 0
}
func devSafeText(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// Terminal-native accents keep the user's palette in control. Selection also
// keeps its textual marker, so the same hierarchy works without color.
func devAccent(s styler, text string) string { return s.paint("\x1b[1;36m", text) }
func devStrong(s styler, text string) string { return s.paint("\x1b[1m", text) }
func devRight(text string, width int) string {
	text = ansi.Truncate(text, width, "…")
	return strings.Repeat(" ", max(0, width-ansi.StringWidth(text))) + text
}
func devFooter(s styler, text string) string {
	keys := map[string]bool{"↑/↓": true, "a": true, "s": true, "m": true, "Tab": true, "/": true, "o": true, "r": true, "x": true, "u": true, "q": true, "?": true, "f": true, "PgUp/PgDn": true, "PgUp": true, "Enter": true, "Esc": true, "Ctrl-C": true, "^C": true}
	var out strings.Builder
	// Keep authored spacing while giving the actual keys a distinct weight.
	for _, part := range strings.SplitAfter(text, " ") {
		word := strings.TrimSuffix(part, " ")
		if keys[word] {
			out.WriteString(devAccent(s, word))
		} else {
			out.WriteString(s.dim(word))
		}
		if strings.HasSuffix(part, " ") {
			out.WriteByte(' ')
		}
	}
	return out.String()
}
func devFit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = ansi.Truncate(text, width, "…")
	return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
}
func (m *devModel) logWidth() int {
	width := m.width - 4
	if m.width >= 94 {
		width -= 29
	}
	return max(10, width)
}
func (m *devModel) logLines(width int) []string {
	var app devAppView
	events := m.sessionLogs.logs
	if m.selected == devShinyHub {
		events = m.hubLogs.logs
	}
	if m.selected >= 0 {
		app = m.apps[m.selected]
		events = app.logs
	}
	var result []string
	for _, event := range events {
		result = append(result, m.eventLogLines(event, app, width)...)
	}
	return result
}
func (m *devModel) eventLogLines(event localrun.Event, app devAppView, width int) []string {
	var result []string

	runtime := event.Source == "app" && event.Attempt == app.servingAttempt
	if m.selected >= 0 && !m.latest && !runtime {
		return nil
	}
	if m.selected >= 0 && m.latest && (event.Attempt != app.latestAttempt || runtime) {
		return nil
	}
	message := event.Message
	if event.Type == "phase" {
		message = event.Phase
		if event.Message != "" {
			message += " · " + event.Message
		}
	}
	if m.filter != "" && !strings.Contains(strings.ToLower(event.App+" "+event.Source+" "+message), strings.ToLower(m.filter)) {
		return nil
	}
	prefix := event.At.Local().Format("15:04:05") + "  "
	if event.At.IsZero() {
		prefix = "          "
	}
	source := ""
	if m.selected >= 0 && m.latest && event.Source == "app" {
		source = "[startup] "
	}
	if m.selected < 0 {
		identity := event.App
		if identity == "" {
			identity = "session"
		}
		if event.Attempt > 0 {
			identity += fmt.Sprintf(" #%d", event.Attempt)
		}
		source = "[" + identity + "] "
		if m.selected == devAllApps && event.Source != "app" {
			source += "hub · "
		}
	}
	if event.Phase == "failed" {
		message = m.style.red(message)
	} else if event.Stream == "stderr" {
		message = m.style.yellow(message)
	}
	wrapped := strings.Split(ansi.Hardwrap(m.style.dim(prefix+source)+message, width, true), "\n")
	result = append(result, wrapped...)

	return result
}

// paneLayout is shared by rendering and navigation so wrapped resource pages
// have exactly the same viewport bounds as the screen the user sees.
func (m *devModel) paneLayout(paneWidth, h int) ([]string, string, []string, int, int) {
	var app devAppView
	if m.selected >= 0 {
		app = m.apps[m.selected]
	}
	s, inner := m.style, m.width-4
	var pane []string
	title := devStrong(s, devSafeText(app.slug))
	if m.selected >= 0 {
		pane = append(pane, title)
		serving := "No healthy version yet"
		if app.stopped {
			serving = "Not serving"
		}
		if app.generation > 0 {
			serving = fmt.Sprintf("Serving v%d", app.generation)
			if app.stopped {
				serving = fmt.Sprintf("Not serving · last healthy v%d", app.generation)
				if app.suspended {
					serving = fmt.Sprintf("Stopped · last healthy v%d", app.generation)
				}
			}
		}
		if app.generation > 0 && !app.stopped {
			serving = s.green(serving)
		} else {
			serving = s.dim(serving)
		}
		latest := ""
		switch app.phase {
		case "ready":
			latest = "Latest change ready"
		case "failed":
			latest = s.red("Latest change failed")
		case "stopped":
			latest = "Reloads suspended · u to resume"
		case "exited":
			latest = s.red("App exited")
		default:
			latest = s.yellow(app.activity)
		}
		pane = append(pane, serving+"   ·   "+latest)
		if !m.showResources {
			if app.url != "" {
				pane = append(pane, s.dim(app.url))
			} else {
				pane = append(pane, s.dim("URL available after the first healthy start"))
			}
		}
		success := "Browser refresh after healthy reloads"
		if !app.lastReady.IsZero() {
			success = "Last ready " + app.lastReady.Local().Format("15:04:05") + "   ·   Browser refresh on"
		}
		if app.stopped {
			success = "Browser refresh after the next healthy start"
			if !app.lastReady.IsZero() {
				success = "Last ready " + app.lastReady.Local().Format("15:04:05")
			}
		}
		if h >= 20 && !m.showResources {
			pane = append(pane, s.dim(success))
			if !m.showResources {
				if app.servingAttempt > 0 && !app.stopped {
					pane = append(pane, s.dim("Serving · "+m.processResourceText(app, app.servingAttempt)))
				}
				if app.latestAttempt != app.servingAttempt && app.processes[app.latestAttempt] != 0 {
					pane = append(pane, s.dim("Startup · "+m.processResourceText(app, app.latestAttempt)))
				}
			}
			pane = append(pane, "")
		}
		if app.failure != "" {
			failure := strings.Split(ansi.Wrap("Failed: "+app.failure, paneWidth, ""), "\n")
			failureLines := 2
			if h < 20 {
				failureLines = 1
			}
			for _, line := range failure[:min(failureLines, len(failure))] {
				pane = append(pane, s.red(line))
			}
			recovery := "Fix the source and save, or press r to restart."
			if paneWidth < 50 {
				recovery = "Fix and save, or r to restart."
			}
			pane = append(pane, s.dim(recovery))
			if h >= 20 {
				pane = append(pane, "")
			}
		}
	} else {
		name, description := "All apps", "Runtime, startup, and ShinyHub events"
		if m.selected == devShinyHub {
			name, description = "ShinyHub", "Workspace, dependencies, readiness, and reloads"
		}
		title = devStrong(s, name)
		pane = append(pane, title)
		if !m.showResources {
			pane = append(pane, s.dim(description))
		}
		healthy, failed := 0, 0
		for _, item := range m.apps {
			if item.generation > 0 && !item.stopped {
				healthy++
			}
			if item.failure != "" {
				failed++
			}
		}
		status := fmt.Sprintf("%d/%d apps serving", healthy, len(m.apps))
		if healthy == len(m.apps) {
			status = s.green(status)
		}
		if failed > 0 {
			status += "  ·  " + s.red(fmt.Sprintf("%d latest changes failed", failed))
		}
		pane = append(pane, status)
		if h >= 20 {
			pane = append(pane, "")
		}
	}
	if m.actionError != "" {
		if h < 20 && app.failure != "" {
			pane[len(pane)-1] = s.red(m.actionError)
		} else {
			pane = append(pane, s.red(m.actionError))
		}
		if h >= 20 {
			pane = append(pane, "")
		}
	}
	runtimeTab, latestTab := " Runtime ", " Latest change "
	if m.latest {
		latestTab = devAccent(s, "[Latest change]")
	} else {
		runtimeTab = devAccent(s, "[Runtime]")
	}
	following := "following"
	if !m.following {
		following = "PAUSED · f to follow"
	}
	if m.showResources {
		pane = append(pane, devAccent(s, "[Resources]")+s.dim("  ·  m returns to logs"))
	} else if m.selected >= 0 {
		pane = append(pane, runtimeTab+"  "+latestTab+"   "+s.dim(following))
	} else {
		pane = append(pane, s.dim(following))
	}
	if m.filter != "" || m.searching {
		pane = append(pane, s.dim("Filter: "+m.filter+"  (Esc clears)"))
	}
	pane = append(pane, s.dim(strings.Repeat("─", paneWidth)))
	// Header and two footer rows reserve six lines. Log viewport fills the rest.
	hostLines := []string{}
	if h >= 20 && !m.showResources {
		hostLines = m.hostResourceLines(inner)
	}
	bodyHeight := h - 6 - len(hostLines)
	// In short terminals, preserve failure context and at least one log row.
	if bodyHeight < len(pane)+1 {
		hostLines = nil
		bodyHeight = h - 6
	}
	for i := len(pane) - 1; i >= 0 && len(pane) >= bodyHeight; i-- {
		if pane[i] == "" {
			pane = append(pane[:i], pane[i+1:]...)
		}
	}
	logHeight := max(1, bodyHeight-len(pane))
	return pane, title, hostLines, bodyHeight, logHeight
}

func (m *devModel) View() tea.View {
	w, h := m.width, m.height
	if len(m.apps) == 0 {
		return tea.NewView("No local apps selected")
	}
	if w < 42 || h < 14 {
		v := tea.NewView("ShinyHub dev\n\nEnlarge the terminal to at least 42 × 14.\nApps keep running. Press q to stop.")
		v.AltScreen = true
		return v
	}
	s := m.style
	inner := w - 4
	sidebar := w >= 94
	sideWidth := 26
	paneWidth := inner
	if sidebar {
		paneWidth -= sideWidth + 3
	}
	pane, title, hostLines, bodyHeight, logHeight := m.paneLayout(paneWidth, h)
	var logs []string
	if !m.showResources {
		logs = m.logLines(paneWidth)
	}
	if m.showResources {
		logs = m.resourceDisplayLines(paneWidth)
	}
	if len(logs) == 0 {
		empty := "Waiting for runtime output."
		if m.latest {
			empty = "No output for the latest change yet."
		}
		if m.selected < 0 {
			empty = "Waiting for session events."
		}
		if m.filter != "" {
			empty = "No log lines match this filter."
		}
		logs = []string{s.dim(empty)}
	}
	end := len(logs)
	if !m.following {
		offset := min(m.offset, max(0, len(logs)-logHeight))
		end -= offset
	}
	start := max(0, end-logHeight)
	if m.showResources {
		mOffset := min(m.resourceOffset, max(0, len(logs)-logHeight))
		start = mOffset
		end = min(len(logs), start+logHeight)
	}
	pane = append(pane, logs[start:end]...)
	for len(pane) < bodyHeight {
		pane = append(pane, "")
	}
	var lines []string
	label := "LOCAL"
	if len(m.apps) > 1 {
		label = fmt.Sprintf("LOCAL · %d apps", len(m.apps))
	}
	header := devAccent(s, "ShinyHub") + s.dim(" / ") + devStrong(s, "dev")
	lines = append(lines, "  "+devFit(header, max(0, inner-ansi.StringWidth(label)))+s.dim(label))
	for _, line := range hostLines {
		lines = append(lines, "  "+s.dim(devFit(line, inner)))
	}
	lines = append(lines, "")
	if sidebar {
		side := []string{devStrong(s, "Views"), ""}
		// The two session scopes precede the apps; keep the selection visible.
		first := max(devAllApps, m.selected-(bodyHeight-3))
		for i := first; i < len(m.apps) && len(side) < bodyHeight; i++ {
			name, status := "All apps", "a"
			if i == devShinyHub {
				name, status = "ShinyHub", "s"
			}
			if i >= 0 {
				item := m.apps[i]
				name, status = devSafeText(item.slug), "starting"
				if item.generation > 0 {
					status = s.green("serving")
				}
				if item.phase == "failed" {
					status = s.red("failed")
				}
				if item.stopped {
					status = "starting"
					if item.phase == "stopping" {
						status = "stopping"
					}
					if item.suspended {
						status = "stopped"
					} else if item.failure != "" {
						status = s.red("failed")
					}
				}
			}
			marker := "  "
			if i == m.selected {
				marker = "> "
			}
			row := marker + devFit(name, 12) + " " + status
			if i == m.selected {
				row = devAccent(s, marker+devFit(name, 12)) + " " + status
			} else {
				row = s.dim(marker) + devFit(name, 12) + " " + status
			}
			side = append(side, row)
		}
		lastContent := len(side) - 1
		for i, line := range pane {
			if line != "" {
				lastContent = max(lastContent, i)
			}
		}
		for i := 0; i < bodyHeight; i++ {
			left := ""
			if i < len(side) {
				left = side[i]
			}
			separator := s.dim(" │ ")
			if i > lastContent {
				separator = "   "
			}
			lines = append(lines, "  "+devFit(left, sideWidth)+separator+devFit(pane[i], paneWidth))
		}
	} else {
		pane[0] = title + s.dim("  ·  ↑/↓ views · a all · s hub")
		for i := 0; i < bodyHeight; i++ {
			lines = append(lines, "  "+devFit(pane[i], paneWidth))
		}
	}
	lines = append(lines, "")
	footer := "↑/↓ views  m metrics  Tab logs  / filter  o open  r restart  x stop  u resume  q quit"
	if inner < 100 {
		footer = "a all  s hub  m metrics  ? controls  q quit"
	}
	if inner < 60 {
		footer = "a all  s hub  m stats  ? help  q quit"
	}
	if m.showResources {
		footer = "m logs · PgUp/PgDn scroll · r restart · x stop · u resume · q quit"
		if inner < 60 {
			footer = "m logs  PgUp/PgDn scroll  ? help  q quit"
			if inner < 42 {
				footer = "m logs  PgUp/PgDn scroll  q quit"
			}
		}
	}
	if m.searching {
		footer = "Type to filter logs   Enter apply   Esc clear   Ctrl-C stop"
		if inner < 60 {
			footer = "^C stop · Enter apply · Esc clear"
		}
	}
	if m.help {
		footer = "r restart · x stop · u resume · a all · s hub · o open · PgUp/PgDn scroll · f follow · q quit"
		if inner < 60 {
			footer = "r restart  x stop  u resume  q quit"
		}
	}
	if m.ended {
		footer = "Session ended · q quit"
	}
	if ansi.StringWidth(footer) > inner {
		switch {
		case m.help:
			footer = "r restart  x stop  u resume  q quit"
		case m.searching:
			footer = "^C stop · Enter apply · Esc clear"
		case m.showResources:
			footer = "m logs  PgUp/PgDn scroll  q quit"
		default:
			footer = "a all  s hub  m stats  ? help  q quit"
		}
	}
	lines = append(lines, "  "+devFit(devFooter(s, footer), inner))
	v := tea.NewView(strings.Join(lines, "\n"))
	v.AltScreen = true
	return v
}
