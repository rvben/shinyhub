// Package testhome runs a test binary against a private home directory, so
// nothing a test reaches through os.UserHomeDir, os.UserCacheDir or
// os.UserConfigDir can read or write the developer's real profile.
//
// A package whose code derives paths from those lookups calls Run from its
// TestMain and Require from one guard test. The guard fails the moment a
// later change drops the redirect, which is how a suite that quietly wrote
// lock files into ~/Library/Caches for months is kept from coming back.
//
// Tests that exec external tools (python3, git) should run them from a
// temporary directory, not from the package directory. Version-manager shims
// such as mise resolve their configuration by walking up from the working
// directory, and once HOME points elsewhere the developer's real global
// config, found on that walk, is treated as an untrusted project file.
package testhome

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// root is the private home directory Run created, or "" before Run.
var root string

// Run points every user-directory lookup at a fresh temporary home, runs m,
// removes the home again and returns the exit code for os.Exit.
func Run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "shinyhub-testhome-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome: create private home:", err)
		return 1
	}
	defer os.RemoveAll(dir)
	for name, value := range variables(dir) {
		if err := os.Setenv(name, value); err != nil {
			fmt.Fprintf(os.Stderr, "testhome: set %s: %v\n", name, err)
			return 1
		}
	}
	root = dir
	return m.Run()
}

// variables lists the environment the standard library consults for the
// user's home, cache and config directories on this platform.
func variables(home string) map[string]string {
	vars := map[string]string{
		"HOME":            home,
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
	}
	if runtime.GOOS == "windows" {
		vars["USERPROFILE"] = home
		vars["LocalAppData"] = filepath.Join(home, "AppData", "Local")
		vars["AppData"] = filepath.Join(home, "AppData", "Roaming")
	}
	return vars
}

// Require fails t unless Run was called and every user-directory lookup now
// resolves inside the private home it created.
func Require(t testing.TB) {
	t.Helper()
	if root == "" {
		t.Fatal("testhome.Run was not called from this package's TestMain, so tests reach the real user directories")
	}
	lookups := []struct {
		name string
		fn   func() (string, error)
	}{
		{"os.UserHomeDir", os.UserHomeDir},
		{"os.UserCacheDir", os.UserCacheDir},
		{"os.UserConfigDir", os.UserConfigDir},
	}
	for _, l := range lookups {
		dir, err := l.fn()
		if err != nil {
			t.Errorf("%s: %v", l.name, err)
			continue
		}
		if !within(dir, root) {
			t.Errorf("%s resolves to %s, outside the private test home %s", l.name, dir, root)
		}
	}
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
