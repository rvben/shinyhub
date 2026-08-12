package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// forceTableOutput pins the human view. Under `go test` stdout is not a
// terminal, so commands default to JSON; a table assertion has to ask for the
// table explicitly or it silently checks the wrong renderer.
func forceTableOutput(t *testing.T) {
	t.Helper()
	prev := outputFlagValue
	outputFlagValue = "table"
	t.Cleanup(func() { outputFlagValue = prev })
}

// runCmd executes a freshly built command with the given args and returns its
// stdout, stderr, and error.
func runCmd(t *testing.T, cmd *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// seedHosts saves a set of hosts in order; the last one saved is current.
func seedHosts(t *testing.T, entries ...[3]string) {
	t.Helper()
	for _, e := range entries {
		if err := saveNamedConfig(&cliConfig{Host: e[0], Token: e[1]}, e[2], "alice"); err != nil {
			t.Fatalf("seed %s: %v", e[0], err)
		}
	}
}

func TestHostsCmd_ListsSavedHostsAndMarksCurrent(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t,
		[3]string{"https://prod.example.com", "shk_prod", "prod"},
		[3]string{"https://dev.example.com", "shk_dev", "dev"},
	)

	stdout, _, err := runCmd(t, newHostsCmd())
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}

	var env struct {
		Items []struct {
			Host    string `json:"host"`
			Name    string `json:"name"`
			User    string `json:"user"`
			Current bool   `json:"current"`
			SavedAt string `json:"saved_at"`
		} `json:"items"`
		Total       int    `json:"total"`
		CurrentHost string `json:"current_host"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("hosts output is not the list envelope: %q (%v)", stdout, err)
	}
	if env.Total != 2 || len(env.Items) != 2 {
		t.Fatalf("got total=%d items=%d, want 2 of each", env.Total, len(env.Items))
	}
	// Sorted by host so repeated runs of the same command do not reshuffle.
	if env.Items[0].Host != "https://dev.example.com" || env.Items[1].Host != "https://prod.example.com" {
		t.Errorf("items not in stable host order: %+v", env.Items)
	}
	if env.CurrentHost != "https://dev.example.com" {
		t.Errorf("current_host = %q, want the most recently saved host", env.CurrentHost)
	}
	if !env.Items[0].Current || env.Items[1].Current {
		t.Errorf("current flag on the wrong item: %+v", env.Items)
	}
	if env.Items[1].Name != "prod" || env.Items[1].User != "alice" {
		t.Errorf("saved name/user not reported: %+v", env.Items[1])
	}
	if env.Items[0].SavedAt == "" {
		t.Error("saved_at should be reported so a stale credential is visible")
	}
}

// The whole point of a credentials listing is that it must not leak the
// credentials. This is asserted for every output format, because a projection
// or a table column added later is exactly how a token would start appearing.
func TestHostsCmd_NeverPrintsTokens(t *testing.T) {
	const secret = "shk_this_value_must_never_be_printed"
	// ndjson is included even though this command rejects it (it emits a single
	// document, like every other list command): a rejected format must not echo
	// state either.
	for _, format := range []string{"json", "table", "ndjson"} {
		t.Run(format, func(t *testing.T) {
			isolatedCredentials(t)
			seedHosts(t, [3]string{"https://prod.example.com", secret, "prod"})

			prev := outputFlagValue
			outputFlagValue = format
			t.Cleanup(func() { outputFlagValue = prev })

			stdout, stderr, err := runCmd(t, newHostsCmd())
			if format == "ndjson" {
				if err == nil {
					t.Errorf("expected -o ndjson to be rejected for a single-document command; got %q", stdout)
				}
			} else if err != nil {
				t.Fatalf("hosts: %v", err)
			}
			haystack := stdout + stderr
			if err != nil {
				haystack += err.Error() + hintOf(err)
			}
			if strings.Contains(haystack, secret) {
				t.Fatalf("hosts printed the saved token:\nstdout %q\nstderr %q\nerr %v", stdout, stderr, err)
			}
		})
	}
}

// --fields must not be a way to ask for the token either: it can only project
// fields the command already emits.
func TestHostsCmd_FieldsCannotSelectTheToken(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t, [3]string{"https://prod.example.com", "shk_secret_value", "prod"})

	stdout, _, err := runCmd(t, newHostsCmd(), "--fields", "token")
	if err == nil {
		t.Fatalf("expected --fields token to be rejected; got %q", stdout)
	}
	if strings.Contains(stdout, "shk_secret_value") {
		t.Fatalf("token leaked through --fields: %q", stdout)
	}
	if kind, _ := classify(err); kind != KindValidation {
		t.Errorf("kind = %q, want %q", kind, KindValidation)
	}
}

func TestHostsCmd_EmptyStoreExplainsHowToAddOne(t *testing.T) {
	isolatedCredentials(t)
	forceTableOutput(t)

	stdout, _, err := runCmd(t, newHostsCmd())
	if err != nil {
		t.Fatalf("hosts on an empty store should not be an error: %v", err)
	}
	if !strings.Contains(stdout, "shinyhub connect") {
		t.Errorf("empty listing should name the command that adds a host, got %q", stdout)
	}
}

func TestHostsCmd_TableMarksTheCurrentHost(t *testing.T) {
	isolatedCredentials(t)
	forceTableOutput(t)
	seedHosts(t,
		[3]string{"https://prod.example.com", "shk_prod", "prod"},
		[3]string{"https://dev.example.com", "shk_dev", "dev"},
	)

	stdout, _, err := runCmd(t, newHostsCmd())
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	var currentLine, otherLine string
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.Contains(line, "https://dev.example.com"):
			currentLine = line
		case strings.Contains(line, "https://prod.example.com"):
			otherLine = line
		}
	}
	if currentLine == "" || otherLine == "" {
		t.Fatalf("both hosts should be listed, got %q", stdout)
	}
	if !strings.HasPrefix(currentLine, "*") {
		t.Errorf("current host line should be marked, got %q", currentLine)
	}
	// Two bounds: the marker has to be absent from the other row, or "marked"
	// would be satisfied by marking everything.
	if strings.HasPrefix(otherLine, "*") {
		t.Errorf("non-current host line must not be marked, got %q", otherLine)
	}
}

// A host with no --name renders as "-" rather than as a blank column, so an
// unnamed server is distinguishable from a misaligned table.
func TestHostsCmd_UnnamedHostRendersAsDash(t *testing.T) {
	isolatedCredentials(t)
	forceTableOutput(t)
	if err := saveConfig(&cliConfig{Host: "https://unnamed.example.com", Token: "shk_x"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stdout, _, err := runCmd(t, newHostsCmd())
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "https://unnamed.example.com") {
			if !strings.Contains(line, "-") {
				t.Errorf("unnamed host should render a dash placeholder, got %q", line)
			}
			return
		}
	}
	t.Fatalf("host not listed: %q", stdout)
}

// "Which servers am I signed in to?" is asked most often when a server is
// down, so neither hosts nor use may depend on reaching one. The positive
// control proves the harness can actually see a request when one is made -
// without it, "no request" would also be the result of a broken observer.
func TestHostsAndUse_NeverContactTheServer(t *testing.T) {
	isolatedCredentials(t)

	var contacted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted = true
		_, _ = w.Write([]byte(`{"user":{"username":"alice","role":"admin"}}`))
	}))
	t.Cleanup(srv.Close)

	seedHosts(t,
		[3]string{srv.URL, "shk_live", "live"},
		[3]string{"https://other.example.com", "shk_other", "other"},
	)

	if _, _, err := runCmd(t, newHostsCmd()); err != nil {
		t.Fatalf("hosts: %v", err)
	}
	if _, _, err := runCmd(t, newUseCmd(), "live"); err != nil {
		t.Fatalf("use: %v", err)
	}
	if contacted {
		t.Error("hosts/use contacted the server; both must answer from local state")
	}

	// Positive control: a command that IS supposed to reach the server does,
	// through the same seam. If this fails, the assertion above proves nothing.
	if _, _, err := runCmd(t, newWhoamiCmd()); err != nil {
		t.Fatalf("whoami (positive control): %v", err)
	}
	if !contacted {
		t.Fatal("positive control failed: the test server never saw whoami's request, so it cannot observe contact at all")
	}
}

func TestUseCmd_SwitchesCurrentHostByNameAndURL(t *testing.T) {
	for _, selector := range []string{"prod", "https://prod.example.com", "HTTPS://Prod.Example.COM/"} {
		t.Run(selector, func(t *testing.T) {
			isolatedCredentials(t)
			seedHosts(t,
				[3]string{"https://prod.example.com", "shk_prod", "prod"},
				[3]string{"https://dev.example.com", "shk_dev", "dev"},
			)

			stdout, _, err := runCmd(t, newUseCmd(), selector)
			if err != nil {
				t.Fatalf("use %s: %v", selector, err)
			}
			var res struct {
				Status string `json:"status"`
				Host   string `json:"host"`
				Name   string `json:"name"`
			}
			if err := json.Unmarshal([]byte(stdout), &res); err != nil {
				t.Fatalf("use output is not an action envelope: %q (%v)", stdout, err)
			}
			if res.Status != "switched" || res.Host != "https://prod.example.com" || res.Name != "prod" {
				t.Errorf("got %+v, want a switch to the prod entry", res)
			}

			// The switch has to survive the process, or it is not a switch.
			st, err := loadStore()
			if err != nil {
				t.Fatalf("loadStore: %v", err)
			}
			if st.CurrentHost != "https://prod.example.com" {
				t.Errorf("persisted current host = %q, want %q", st.CurrentHost, "https://prod.example.com")
			}
			// The other credential must still be there.
			if st.Hosts["https://dev.example.com"].Token != "shk_dev" {
				t.Error("switching hosts must not disturb the other saved credential")
			}
		})
	}
}

// Switching to the server already in use is a no-op, not an error: scripts that
// set their target defensively should not have to branch on it.
func TestUseCmd_AlreadyCurrentIsUnchanged(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t, [3]string{"https://prod.example.com", "shk_prod", "prod"})

	stdout, _, err := runCmd(t, newUseCmd(), "prod")
	if err != nil {
		t.Fatalf("use: %v", err)
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("not an action envelope: %q (%v)", stdout, err)
	}
	if res.Status != "unchanged" {
		t.Errorf("status = %q, want %q so callers can tell a no-op from a switch", res.Status, "unchanged")
	}
}

func TestUseCmd_UnknownNameListsWhatIsSaved(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t, [3]string{"https://prod.example.com", "shk_prod", "prod"})

	_, _, err := runCmd(t, newUseCmd(), "staging")
	if err == nil {
		t.Fatal("expected an error for an unknown name")
	}
	if kind, code := classify(err); kind != KindValidation || code != 1 {
		t.Errorf("classify = (%q,%d), want (%q,1)", kind, code, KindValidation)
	}
	if hint := hintOf(err); !strings.Contains(hint, "prod") {
		t.Errorf("hint should list the saved hosts, got %q", hint)
	}
}

// A URL that parses fine but has no saved credential must not become the
// current host: the next command would fail with a second, less obvious error.
func TestUseCmd_RefusesAHostWithNoSavedCredential(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t, [3]string{"https://prod.example.com", "shk_prod", "prod"})

	_, _, err := runCmd(t, newUseCmd(), "https://never-logged-in.example.com")
	if err == nil {
		t.Fatal("expected an error for a host with no credential")
	}
	if kind, code := classify(err); kind != KindAuth || code != 3 {
		t.Errorf("classify = (%q,%d), want (%q,3)", kind, code, KindAuth)
	}
	if hint := hintOf(err); !strings.Contains(hint, "shinyhub connect https://never-logged-in.example.com") {
		t.Errorf("hint should offer logging in to that host, got %q", hint)
	}

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if st.CurrentHost != "https://prod.example.com" {
		t.Errorf("a refused switch must leave the current host alone, got %q", st.CurrentHost)
	}
}

func TestUseCmd_RequiresExactlyOneArgument(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t, [3]string{"https://prod.example.com", "shk_prod", "prod"})

	if _, _, err := runCmd(t, newUseCmd()); err == nil {
		t.Error("expected an error with no argument")
	}
	if _, _, err := runCmd(t, newUseCmd(), "a", "b"); err == nil {
		t.Error("expected an error with two arguments")
	}
}

// The global --host means "target this server for one command". Neither hosts
// nor use contacts a server, so accepting it silently would let the user
// believe they had scoped something they had not.
func TestHostsAndUse_RefuseTheGlobalHostFlag(t *testing.T) {
	isolatedCredentials(t)
	seedHosts(t,
		[3]string{"https://prod.example.com", "shk_prod", "prod"},
		[3]string{"https://dev.example.com", "shk_dev", "dev"},
	)

	t.Run("hosts", func(t *testing.T) {
		hostFlagOverride = "prod"
		t.Cleanup(func() { hostFlagOverride = "" })
		if _, _, err := runCmd(t, newHostsCmd()); err == nil {
			t.Fatal("expected hosts to refuse --host")
		} else if hint := hintOf(err); !strings.Contains(hint, "whoami") {
			t.Errorf("hint should point at the command that does take --host, got %q", hint)
		}
	})

	t.Run("use with an argument", func(t *testing.T) {
		hostFlagOverride = "dev"
		t.Cleanup(func() { hostFlagOverride = "" })
		if _, _, err := runCmd(t, newUseCmd(), "prod"); err == nil {
			t.Fatal("expected use to refuse a --host that contradicts its argument")
		}
		// The refused command must not have switched anything.
		st, _ := loadStore()
		if st.CurrentHost != "https://dev.example.com" {
			t.Errorf("current host = %q, want it unchanged", st.CurrentHost)
		}
	})

	// The likely mistake: reaching for --host because every other command uses
	// it to pick a server. The error has to name the command that works.
	t.Run("use with only --host", func(t *testing.T) {
		hostFlagOverride = "prod"
		t.Cleanup(func() { hostFlagOverride = "" })
		_, _, err := runCmd(t, newUseCmd())
		if err == nil {
			t.Fatal("expected an error when the server is given only via --host")
		}
		if hint := hintOf(err); !strings.Contains(hint, "shinyhub use prod") {
			t.Errorf("hint should show the working command, got %q", hint)
		}
	})
}
