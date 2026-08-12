package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func completionTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "shinyhub"}
	AddCommandsTo(root)
	return root
}

func TestCompletionInstallZshIsSafeIdempotentAndReversible(t *testing.T) {
	for _, original := range [][]byte{[]byte("export EDITOR=vim\n"), []byte("export EDITOR=vim")} {
		t.Run(string(original), func(t *testing.T) {
			home := t.TempDir()
			configHome := filepath.Join(home, "xdg")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("ZDOTDIR", "")
			t.Setenv("SHELL", "/bin/zsh")

			rcPath := filepath.Join(home, ".zshrc")
			if err := os.WriteFile(rcPath, original, 0600); err != nil {
				t.Fatal(err)
			}
			root := completionTestRoot()
			var out bytes.Buffer
			if err := installCompletion(root, "zsh", false, &out); err != nil {
				t.Fatalf("install: %v", err)
			}
			scriptPath := filepath.Join(configHome, "shinyhub", "completions", "shinyhub.zsh")
			script, err := os.ReadFile(scriptPath)
			if err != nil || !bytes.Contains(script, []byte("_shinyhub")) {
				t.Fatalf("generated zsh script is missing: err=%v", err)
			}
			rcAfterFirst, err := os.ReadFile(rcPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"export EDITOR=vim", completionBlockStart, scriptPath, completionBlockEnd} {
				if !strings.Contains(string(rcAfterFirst), want) {
					t.Errorf("installed .zshrc missing %q:\n%s", want, rcAfterFirst)
				}
			}
			if info, err := os.Stat(rcPath); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("startup-file mode changed: info=%v err=%v", info, err)
			}

			out.Reset()
			if err := installCompletion(root, "zsh", false, &out); err != nil {
				t.Fatalf("second install: %v", err)
			}
			rcAfterSecond, _ := os.ReadFile(rcPath)
			if !bytes.Equal(rcAfterFirst, rcAfterSecond) {
				t.Fatal("reinstall changed an already-current startup file")
			}
			if strings.Count(string(rcAfterSecond), completionBlockStart) != 1 {
				t.Fatalf("reinstall duplicated the managed block:\n%s", rcAfterSecond)
			}
			if !strings.Contains(out.String(), "already up to date") {
				t.Errorf("reinstall output = %q", out.String())
			}

			if err := uninstallCompletion("zsh", false, &out); err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			rcAfterUninstall, _ := os.ReadFile(rcPath)
			if !bytes.Equal(rcAfterUninstall, original) {
				t.Fatalf("uninstall did not restore user content: got %q want %q", rcAfterUninstall, original)
			}
			if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
				t.Fatalf("completion script remains after uninstall: %v", err)
			}
		})
	}
}

func TestCompletionInstallFishUsesNativeDirectoryWithoutStartupEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	root := completionTestRoot()
	if err := installCompletion(root, "fish", false, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config", "fish", "completions", "shinyhub.fish")
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(b, []byte("complete -c shinyhub")) {
		t.Fatalf("fish completion not installed correctly: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "fish", "config.fish")); !os.IsNotExist(err) {
		t.Fatalf("fish startup file should not be created: %v", err)
	}
}

func TestCompletionInstallDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	previousGOOS := completionGOOS
	completionGOOS = "linux"
	t.Cleanup(func() { completionGOOS = previousGOOS })
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	var out bytes.Buffer
	if err := installCompletion(completionTestRoot(), "bash", true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "dry run") || !strings.Contains(out.String(), ".bashrc") {
		t.Errorf("dry-run output = %q", out.String())
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dry run wrote files: %v", entries)
	}
}

func TestCompletionGenerationSupportsEveryDocumentedShell(t *testing.T) {
	root := completionTestRoot()
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			var out bytes.Buffer
			if err := generateCompletion(root, shell, &out); err != nil {
				t.Fatal(err)
			}
			if out.Len() < 100 || !strings.Contains(strings.ToLower(out.String()), "shinyhub") {
				t.Fatalf("generated %s completion is unexpectedly empty", shell)
			}
		})
	}
}

func TestRequestedCompletionShellDetectsAliasesAndGuidesUnknownShell(t *testing.T) {
	t.Setenv("SHELL", "/opt/homebrew/bin/zsh")
	if got, err := requestedCompletionShell(nil); err != nil || got != "zsh" {
		t.Fatalf("detected shell = %q, err=%v", got, err)
	}
	if got := normalizeCompletionShell(`C:\\Program Files\\PowerShell\\7\\pwsh.exe`); got != "powershell" {
		t.Fatalf("pwsh alias normalized to %q", got)
	}
	if _, err := requestedCompletionShell([]string{"nu"}); err == nil || !strings.Contains(hintOf(err), "bash, zsh, fish, or powershell") {
		t.Fatalf("unknown-shell error = %v hint=%q", err, hintOf(err))
	}
}

func TestCompletionInstallRefusesAmbiguousManagedBlock(t *testing.T) {
	tests := map[string][]byte{
		"incomplete": []byte("export EDITOR=vim\n" + completionBlockStart + "\nsource missing\n"),
		"duplicated": []byte(completionBlockStart + "\none\n" + completionBlockEnd + "\n" + completionBlockStart + "\ntwo\n" + completionBlockEnd + "\n"),
	}
	for name, broken := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			rcPath := filepath.Join(home, ".zshrc")
			if err := os.WriteFile(rcPath, broken, 0600); err != nil {
				t.Fatal(err)
			}
			err := installCompletion(completionTestRoot(), "zsh", false, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "malformed or duplicated") {
				t.Fatalf("error = %v", err)
			}
			after, _ := os.ReadFile(rcPath)
			if !bytes.Equal(after, broken) {
				t.Fatal("installer modified an ambiguous user-edited startup file")
			}
			if _, err := os.Stat(filepath.Join(home, "config", "shinyhub", "completions", "shinyhub.zsh")); !os.IsNotExist(err) {
				t.Fatalf("installer wrote a script before validating the startup file: %v", err)
			}
		})
	}
}

func TestCompleteSavedHostsOffersAliasesAndURLsWithoutTokens(t *testing.T) {
	isolatedCredentials(t)
	st := &credentialStore{CurrentHost: "https://prod.example.com", Hosts: map[string]hostCredential{
		"https://prod.example.com":  {Name: "prod", Token: "shk_never_complete_me"},
		"https://stage.example.com": {Name: "stage", Token: "shk_also_private"},
	}}
	if err := saveStore(st); err != nil {
		t.Fatal(err)
	}
	values, directive := completeSavedHosts(nil, nil, "")
	joined := strings.Join(values, "\n")
	for _, want := range []string{"prod\thttps://prod.example.com", "https://stage.example.com\tstage"} {
		if !strings.Contains(joined, want) {
			t.Errorf("completions missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "shk_") {
		t.Fatalf("host completions leaked a credential: %s", joined)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v", directive)
	}
}
