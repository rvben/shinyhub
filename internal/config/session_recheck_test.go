package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

// loadWithYAML writes a config file and loads it, with the auth secret supplied
// out of band as every config test does.
func loadWithYAML(t *testing.T, yaml string) *config.Config {
	t.Helper()
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	path := ""
	if yaml != "" {
		path = filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// An operator who never heard of this knob still gets revocation enforced on
// live sessions. That is the whole point of defaulting it on.
func TestSessionRecheckInterval_DefaultsToThirtySeconds(t *testing.T) {
	cfg := loadWithYAML(t, "")
	if got := cfg.SessionRecheckInterval(); got != 30*time.Second {
		t.Fatalf("SessionRecheckInterval() = %v, want 30s", got)
	}
}

func TestSessionRecheckInterval_ExplicitValueWins(t *testing.T) {
	cfg := loadWithYAML(t, "server:\n  session_recheck_interval: 5s\n")
	if got := cfg.SessionRecheckInterval(); got != 5*time.Second {
		t.Fatalf("SessionRecheckInterval() = %v, want 5s", got)
	}
}

// Unlike the timeouts around it, 0 is meaningful here: it turns the sweep off.
// An operator must be able to say that explicitly, and it must not be confused
// with the key being absent.
func TestSessionRecheckInterval_ExplicitZeroDisablesTheSweep(t *testing.T) {
	cfg := loadWithYAML(t, "server:\n  session_recheck_interval: 0\n")
	if got := cfg.SessionRecheckInterval(); got != 0 {
		t.Fatalf("SessionRecheckInterval() = %v, want 0 (disabled)", got)
	}
}

// The reason a bare 0 is accepted is that zero is the same span in every unit.
// A bare 30 is not: read as nanoseconds it would sweep 30 million times a
// second, read as seconds it is what the operator meant. Refuse to guess, and
// say what to write instead.
func TestSessionRecheckInterval_BareNumberIsRejected(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  session_recheck_interval: 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load must reject a unit-less session_recheck_interval")
	}
	if !strings.Contains(err.Error(), `"30s"`) {
		t.Fatalf("error must name the spelling that works, got: %v", err)
	}
}

// No YAML spelling may disable a security sweep without saying so. The trap
// this guards is specific: yaml truncates a float into an int, so decoding
// through an int would turn "0.5" into 0 and silently switch the sweep off
// while the operator believed they had asked for a half-second one.
func TestSessionRecheckInterval_NoValueSilentlyDisablesTheSweep(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
		err   bool
	}{
		{name: "fractional seconds are rejected, not truncated to off", value: "0.5", err: true},
		{name: "unit-less integer is rejected", value: "30", err: true},
		{name: "boolean is rejected", value: "true", err: true},
		{name: "list is rejected", value: "[30s]", err: true},
		{name: "explicit zero disables", value: "0", want: 0},
		{name: "explicit zero as a float disables", value: "0.0", want: 0},
		{name: "quoted zero disables", value: `"0"`, want: 0},
		{name: "absent applies the default", value: "null", want: 30 * time.Second},
		{name: "empty applies the default", value: "", want: 30 * time.Second},
		{name: "quoted duration is honoured", value: `"45s"`, want: 45 * time.Second},
		{name: "fractional duration with a unit is honoured", value: "500ms", want: 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
			path := filepath.Join(t.TempDir(), "config.yaml")
			body := "server:\n  session_recheck_interval: " + tc.value + "\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if tc.err {
				if err == nil {
					t.Fatalf("Load(%q) must fail rather than silently pick an interval, got %v", tc.value, cfg.SessionRecheckInterval())
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(%q): %v", tc.value, err)
			}
			if got := cfg.SessionRecheckInterval(); got != tc.want {
				t.Fatalf("SessionRecheckInterval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSessionRecheckInterval_NegativeDisablesTheSweep(t *testing.T) {
	cfg := loadWithYAML(t, "server:\n  session_recheck_interval: -1s\n")
	if got := cfg.SessionRecheckInterval(); got != 0 {
		t.Fatalf("SessionRecheckInterval() = %v, want 0 (disabled)", got)
	}
}

func TestSessionRecheckInterval_EnvOverridesYAML(t *testing.T) {
	t.Setenv("SHINYHUB_SESSION_RECHECK_INTERVAL", "90s")
	cfg := loadWithYAML(t, "server:\n  session_recheck_interval: 5s\n")
	if got := cfg.SessionRecheckInterval(); got != 90*time.Second {
		t.Fatalf("SessionRecheckInterval() = %v, want 90s", got)
	}
}

func TestSessionRecheckInterval_EnvZeroDisablesTheSweep(t *testing.T) {
	t.Setenv("SHINYHUB_SESSION_RECHECK_INTERVAL", "0")
	cfg := loadWithYAML(t, "")
	if got := cfg.SessionRecheckInterval(); got != 0 {
		t.Fatalf("SessionRecheckInterval() = %v, want 0 (disabled)", got)
	}
}

// A typo must be reported, not silently swallowed into the default: an operator
// who meant to shorten the interval would otherwise never learn it did nothing.
func TestSessionRecheckInterval_UnparsableEnvIsAnError(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	t.Setenv("SHINYHUB_SESSION_RECHECK_INTERVAL", "30 seconds")
	if _, err := config.Load(""); err == nil {
		t.Fatal("Load must reject an unparsable SHINYHUB_SESSION_RECHECK_INTERVAL")
	}
}
