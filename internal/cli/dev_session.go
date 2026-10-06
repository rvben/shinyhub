package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rvben/shinyhub/internal/localrun"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type devPresentation string

const (
	devPlain  devPresentation = "plain"
	devTUI    devPresentation = "tui"
	devNDJSON devPresentation = "ndjson"
)

// Automatic UI requires every standard stream to be interactive. Redirecting
// stderr must also preserve useful diagnostics rather than hide them in a HUD.
func devInteractive(cmd *cobra.Command) bool {
	in, inok := cmd.InOrStdin().(*os.File)
	out, outok := cmd.OutOrStdout().(*os.File)
	errout, errok := cmd.ErrOrStderr().(*os.File)
	return inok && outok && errok && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd())) && term.IsTerminal(int(errout.Fd())) && os.Getenv("TERM") != "dumb"
}

func resolveDevPresentation(cmd *cobra.Command, f *devFlags) (devPresentation, error) {
	output := ""
	if flag := cmd.Flags().Lookup("output"); flag != nil && flag.Changed {
		output = flag.Value.String()
	}
	if output != "" && output != "table" && output != "ndjson" {
		return "", validationErr("local dev is a continuous stream; --output accepts table or ndjson", "use --output ndjson for structured events")
	}
	if f.tui && cmd.Flags().Changed("tui") && output != "" {
		return "", validationErr("--tui conflicts with --output", "use --tui for the interactive view, or --output for a stream")
	}
	if output == "ndjson" {
		resolvedFormat = formatNDJSON
		return devNDJSON, nil
	}
	if output == "table" {
		resolvedFormat = formatTable
		return devPlain, nil
	}
	if cmd.Flags().Changed("tui") {
		if !f.tui {
			resolvedFormat = formatTable
			return devPlain, nil
		}
		if !devInteractive(cmd) {
			return "", validationErr("--tui requires interactive input, output, and error terminals", "omit --tui or use --tui=false when redirecting output")
		}
		return devTUI, nil
	}
	resolvedFormat = formatTable
	if devInteractive(cmd) && !isCIEnvironment(os.Getenv) {
		return devTUI, nil
	}
	return devPlain, nil
}

type devSession struct {
	emit    func(localrun.Event)
	retries map[string]chan struct{}
}

func (s *devSession) configure(slug string, options *localrun.Options) {
	if s == nil {
		return
	}
	options.OnEvent = s.emit
	options.Reload = s.retries[slug]
}

// The UI owns only presentation and controls. The same runner and cancellation
// path are used by all three output modes, including multi-app fleets.
func runLocalDevPresentation(cmd *cobra.Command, args []string, f *devFlags, scope *devScope) error {
	mode, err := resolveDevPresentation(cmd, f)
	if err != nil {
		return err
	}
	if mode == devPlain {
		return runLocalDev(cmd, args, f, scope)
	}
	slugs := devTargetSlugs(scope.Targets)
	if !scope.fleet() {
		slug, err := resolveLocalRunSlug(devSourceDir(args), f.slug)
		if err != nil {
			return err
		}
		slugs = []string{slug}
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	child := &cobra.Command{}
	// Presentation changes writers, not the parsed flag context. Fleet defaults
	// must still distinguish explicit overrides such as --seed=never.
	child.Flags().AddFlagSet(cmd.Flags())
	child.Flags().AddFlagSet(cmd.InheritedFlags())
	child.SetContext(ctx)
	child.SetOut(io.Discard)
	child.SetErr(cmd.ErrOrStderr()) // pre-run diagnostics stay outside the event stream
	child.SetIn(cmd.InOrStdin())
	// Keep validation attached to the original command's flags before launching
	// a presentation-only child. runLocalDev also checks changed global flags.
	if err := validateLocalDevFlags(cmd); err != nil {
		return err
	}
	session := &devSession{retries: make(map[string]chan struct{}, len(slugs))}
	for _, slug := range slugs {
		session.retries[slug] = make(chan struct{}, 1)
	}
	copyFlags := *f
	copyFlags.session = session
	if mode == devNDJSON {
		var mu sync.Mutex
		var encodeErr error
		encoder := json.NewEncoder(cmd.OutOrStdout())
		session.emit = func(event localrun.Event) {
			mu.Lock()
			defer mu.Unlock()
			if encodeErr == nil {
				encodeErr = encoder.Encode(event)
				if encodeErr != nil {
					cancel()
				}
			}
		}
		err := runLocalDev(child, args, &copyFlags, scope)
		mu.Lock()
		defer mu.Unlock()
		if encodeErr != nil {
			return fmt.Errorf("write development events: %w", encodeErr)
		}
		return err
	}
	// All pre-run diagnostics are captured as log events instead of painting over
	// the alternate screen. Runner output itself is emitted through OnEvent.
	events := make(chan localrun.Event, 256)
	diskPath, _ := filepath.Abs(devSourceDir(args))
	monitor := &devResourceMonitor{diskPath: diskPath}
	resourceUpdates := make(chan devResourceMsg, 1)
	go monitor.run(ctx, resourceUpdates)
	session.emit = func(event localrun.Event) {
		monitor.observe(event)
		select {
		case events <- event:
		case <-ctx.Done():
		}
	}
	diagnostics := &devDiagnosticWriter{emit: session.emit}
	child.SetErr(diagnostics)
	done := make(chan error, 1)
	model := newDevModel(slugs, events, done, session.retries, cancel, stylerFor(cmd.OutOrStdout()))
	model.resourceUpdates = resourceUpdates
	program := tea.NewProgram(model, tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
	go func() {
		err := runLocalDev(child, args, &copyFlags, scope)
		diagnostics.flush()
		done <- err
		model.finished <- err
	}()
	_, uiErr := program.Run()
	cancel()
	// Never return to the shell while an app or producer process is still alive.
	runErr := <-model.finished
	if uiErr != nil {
		return uiErr
	}
	return runErr
}

func validateLocalDevFlags(cmd *cobra.Command) error {
	if changed := changedDevFlags(cmd, devRemoteOnlyFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" require remote development", "add --remote <host> or remove the remote-only flags")
	}
	if changed := changedDevFlags(cmd, devServerGlobalFlags); len(changed) > 0 {
		return validationErr(strings.Join(changed, ", ")+" apply only to remote development", "add --remote <host> or remove the server option")
	}
	return nil
}

type devDiagnosticWriter struct {
	mu       sync.Mutex
	emit     func(localrun.Event)
	fragment string
}

func (w *devDiagnosticWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fragment += string(p)
	for len(w.fragment) > 0 {
		size := strings.IndexByte(w.fragment, '\n')
		if size < 0 && len(w.fragment) < 8192 {
			break
		}
		if size < 0 || size > 8192 {
			size = 8192
		}
		line := w.fragment[:size]
		w.fragment = w.fragment[size:]
		w.fragment = strings.TrimPrefix(w.fragment, "\n")
		w.emit(localrun.Event{Type: "log", At: time.Now().UTC(), Source: "reload", Stream: "stderr", Message: line})
	}
	return len(p), nil
}

func (w *devDiagnosticWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fragment != "" {
		w.emit(localrun.Event{Type: "log", At: time.Now().UTC(), Source: "reload", Stream: "stderr", Message: w.fragment})
		w.fragment = ""
	}
}
