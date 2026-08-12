package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const (
	completionBlockStart = "# >>> shinyhub shell completion >>>"
	completionBlockEnd   = "# <<< shinyhub shell completion <<<"
)

type completionInstallFlags struct {
	dryRun bool
}

type completionLayout struct {
	scriptPath string
	rcPath     string
	rcBlock    string
	reload     string
}

var completionGOOS = runtime.GOOS

func newCompletionCmd(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion",
		Short: "Generate or install shell completions",
		Long: `Generate a completion script for a package manager, or install it for the
current user with one command. Installation is idempotent and uses a small,
clearly marked startup-file block that can be removed with completion uninstall.`,
		Args: cobra.NoArgs,
	}
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		cmd.AddCommand(newCompletionGenerateCmd(root, shell))
	}
	cmd.AddCommand(newCompletionInstallCmd(root), newCompletionUninstallCmd())
	return cmd
}

func newCompletionGenerateCmd(root *cobra.Command, shell string) *cobra.Command {
	return &cobra.Command{
		Use:                   shell,
		Short:                 "Generate the autocompletion script for " + shell,
		Args:                  cobra.NoArgs,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return generateCompletion(root, shell, cmd.OutOrStdout())
		},
	}
}

func newCompletionInstallCmd(root *cobra.Command) *cobra.Command {
	f := &completionInstallFlags{}
	cmd := &cobra.Command{
		Use:   "install [bash|zsh|fish|powershell]",
		Short: "Install completions for the current user",
		Long: `Install generated completions in the current user's configuration.

When the shell argument is omitted, ShinyHub detects it from $SHELL. Bash, zsh,
and PowerShell receive one marked source block in their startup file; fish uses
its native completions directory. Re-running the command safely updates the
existing installation without duplicating startup lines.`,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell, err := requestedCompletionShell(args)
			if err != nil {
				return err
			}
			return installCompletion(root, shell, f.dryRun, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Show the files that would change without writing them")
	return cmd
}

func newCompletionUninstallCmd() *cobra.Command {
	f := &completionInstallFlags{}
	cmd := &cobra.Command{
		Use:       "uninstall [bash|zsh|fish|powershell]",
		Short:     "Remove ShinyHub's installed shell completion",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			shell, err := requestedCompletionShell(args)
			if err != nil {
				return err
			}
			return uninstallCompletion(shell, f.dryRun, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Show the files that would change without writing them")
	return cmd
}

func requestedCompletionShell(args []string) (string, error) {
	raw := ""
	if len(args) == 1 {
		raw = args[0]
	} else {
		raw = os.Getenv("SHELL")
		if raw == "" && completionGOOS == "windows" {
			raw = "powershell"
		}
	}
	shell := normalizeCompletionShell(raw)
	if shell == "" {
		if len(args) == 0 {
			return "", validationErr("could not detect a supported shell from $SHELL",
				"run `shinyhub completion install zsh` (or bash, fish, powershell)")
		}
		return "", validationErr(fmt.Sprintf("unsupported shell %q", args[0]),
			"choose bash, zsh, fish, or powershell")
	}
	return shell, nil
}

func normalizeCompletionShell(raw string) string {
	// Accept Windows-style $SHELL values even when tests or remote tooling run
	// on a Unix host.
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	base := strings.ToLower(filepath.Base(raw))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "bash", "zsh", "fish":
		return base
	case "pwsh", "powershell":
		return "powershell"
	default:
		return ""
	}
}

func generateCompletion(root *cobra.Command, shell string, out io.Writer) error {
	switch shell {
	case "bash":
		return root.GenBashCompletionV2(out, true)
	case "zsh":
		return root.GenZshCompletion(out)
	case "fish":
		return root.GenFishCompletion(out, true)
	case "powershell":
		return root.GenPowerShellCompletionWithDesc(out)
	default:
		return fmt.Errorf("unsupported completion shell %q", shell)
	}
}

func completionPaths(shell string) (completionLayout, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return completionLayout{}, errors.New("find home directory for shell completion")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	layout := completionLayout{}
	switch shell {
	case "fish":
		layout.scriptPath = filepath.Join(configHome, "fish", "completions", "shinyhub.fish")
		layout.reload = "Open a new fish session; fish loads the completion automatically."
	case "zsh":
		layout.scriptPath = filepath.Join(configHome, "shinyhub", "completions", "shinyhub.zsh")
		zdotdir := os.Getenv("ZDOTDIR")
		if zdotdir == "" {
			zdotdir = home
		}
		layout.rcPath = filepath.Join(zdotdir, ".zshrc")
		layout.rcBlock = shellSourceBlock(shell, layout.scriptPath)
		layout.reload = "Start a new shell, or run: source " + shellQuote(layout.rcPath)
	case "bash":
		layout.scriptPath = filepath.Join(configHome, "shinyhub", "completions", "shinyhub.bash")
		layout.rcPath = bashStartupPath(home)
		layout.rcBlock = shellSourceBlock(shell, layout.scriptPath)
		layout.reload = "Start a new shell, or run: source " + shellQuote(layout.rcPath)
	case "powershell":
		profileDir := filepath.Join(configHome, "powershell")
		if completionGOOS == "windows" {
			profileDir = filepath.Join(home, "Documents", "PowerShell")
		}
		layout.scriptPath = filepath.Join(profileDir, "shinyhub-completion.ps1")
		layout.rcPath = filepath.Join(profileDir, "Microsoft.PowerShell_profile.ps1")
		layout.rcBlock = shellSourceBlock(shell, layout.scriptPath)
		layout.reload = "Start a new PowerShell session, or dot-source your profile."
	default:
		return completionLayout{}, fmt.Errorf("unsupported completion shell %q", shell)
	}
	return layout, nil
}

func bashStartupPath(home string) string {
	bashrc := filepath.Join(home, ".bashrc")
	if completionGOOS != "darwin" || completionFileExists(bashrc) {
		return bashrc
	}
	return filepath.Join(home, ".bash_profile")
}

func shellSourceBlock(shell, scriptPath string) string {
	quoted := shellQuote(scriptPath)
	lines := []string{completionBlockStart}
	if shell == "zsh" {
		lines = append(lines, "autoload -Uz compinit", "(( $+functions[compdef] )) || compinit")
	}
	if shell == "powershell" {
		quoted = "'" + strings.ReplaceAll(scriptPath, "'", "''") + "'"
		lines = append(lines, "if (Test-Path "+quoted+") { . "+quoted+" }")
	} else {
		lines = append(lines, "[ ! -f "+quoted+" ] || source "+quoted)
	}
	return strings.Join(append(lines, completionBlockEnd), "\n")
}

func installCompletion(root *cobra.Command, shell string, dryRun bool, out io.Writer) error {
	layout, err := completionPaths(shell)
	if err != nil {
		return err
	}
	var script bytes.Buffer
	if err := generateCompletion(root, shell, &script); err != nil {
		return err
	}
	changed := !fileContentEquals(layout.scriptPath, script.Bytes())
	rcChanged := false
	var rcUpdated []byte
	if layout.rcPath != "" {
		current, readErr := readOptionalFile(layout.rcPath)
		if readErr != nil {
			return readErr
		}
		if managedCompletionBlockMalformed(current) {
			return validationErr("the ShinyHub completion block in "+layout.rcPath+" is malformed or duplicated",
				"remove the marked ShinyHub completion block(s), then rerun `shinyhub completion install "+shell+"`")
		}
		rcUpdated = replaceManagedCompletionBlock(current, layout.rcBlock)
		rcChanged = !bytes.Equal(current, rcUpdated)
	}
	if !dryRun && changed {
		// Install the inert script before making a startup file source it. A
		// failure between the two leaves an unused file, never a broken shell.
		if err := writeUserFile(layout.scriptPath, script.Bytes()); err != nil {
			return err
		}
	}
	if !dryRun && rcChanged {
		if err := writeUserFile(layout.rcPath, rcUpdated); err != nil {
			return err
		}
	}
	state := "already up to date"
	if changed || rcChanged {
		state = "installed"
	}
	if dryRun {
		state = "dry run"
	}
	fmt.Fprintf(out, "Shell completion for %s: %s\n  Script: %s\n", shell, state, layout.scriptPath)
	if layout.rcPath != "" {
		fmt.Fprintf(out, "  Startup file: %s\n", layout.rcPath)
	}
	fmt.Fprintln(out, layout.reload)
	return nil
}

func uninstallCompletion(shell string, dryRun bool, out io.Writer) error {
	layout, err := completionPaths(shell)
	if err != nil {
		return err
	}
	changed := completionFileExists(layout.scriptPath)
	rcChanged := false
	if layout.rcPath != "" {
		current, readErr := readOptionalFile(layout.rcPath)
		if readErr != nil {
			return readErr
		}
		if managedCompletionBlockMalformed(current) {
			return validationErr("the ShinyHub completion block in "+layout.rcPath+" is malformed or duplicated",
				"remove the marked ShinyHub completion block(s) manually, then rerun this command")
		}
		updated := removeManagedCompletionBlock(current)
		rcChanged = !bytes.Equal(current, updated)
		if !dryRun && rcChanged {
			if err := writeUserFile(layout.rcPath, updated); err != nil {
				return err
			}
		}
	}
	if !dryRun && changed {
		if err := os.Remove(layout.scriptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove completion script: %w", err)
		}
	}
	state := "not installed"
	if changed || rcChanged {
		state = "removed"
	}
	if dryRun {
		state = "dry run"
	}
	fmt.Fprintf(out, "Shell completion for %s: %s\n", shell, state)
	return nil
}

func managedCompletionBlockMalformed(current []byte) bool {
	text := string(current)
	starts := strings.Count(text, completionBlockStart)
	ends := strings.Count(text, completionBlockEnd)
	return starts != ends || starts > 1
}

func replaceManagedCompletionBlock(current []byte, block string) []byte {
	without := removeManagedCompletionBlock(current)
	separator := ""
	if len(without) > 0 {
		// This separator belongs to the managed block. uninstall removes this
		// exact byte again, preserving even a user's trailing-newline choice.
		separator = "\n"
	}
	return []byte(string(without) + separator + block + "\n")
}

func removeManagedCompletionBlock(current []byte) []byte {
	text := string(current)
	start := strings.Index(text, completionBlockStart)
	if start < 0 {
		return current
	}
	endRel := strings.Index(text[start:], completionBlockEnd)
	if endRel < 0 {
		// Never discard an unterminated user-edited block.
		return current
	}
	end := start + endRel + len(completionBlockEnd)
	if end < len(text) && text[end] == '\r' {
		end++
	}
	if end < len(text) && text[end] == '\n' {
		end++
	}
	// replaceManagedCompletionBlock always owns one separator newline before a
	// non-leading block. Remove it with the block so uninstall is byte-for-byte
	// reversible for the user's original startup file.
	removeStart := start
	if removeStart > 0 && text[removeStart-1] == '\n' {
		removeStart--
	}
	return []byte(text[:removeStart] + text[end:])
}

func readOptionalFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return b, nil
}

func writeUserFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	// os.Rename cannot replace an existing file on Windows. Keep PowerShell
	// reinstalls working there while preserving the existing file and mode.
	if runtime.GOOS == "windows" && completionFileExists(path) {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		if _, err := file.Write(content); err != nil {
			_ = file.Close()
			return fmt.Errorf("write %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s: %w", path, err)
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".shinyhub-completion-*")
	if err != nil {
		return fmt.Errorf("create temporary completion file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

func fileContentEquals(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	return err == nil && bytes.Equal(got, want)
}

func completionFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
