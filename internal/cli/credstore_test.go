package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatedCredentials points the credentials file at a fresh temp path and
// clears every input that selects a host, so a test starts from "no saved
// hosts, nothing in the environment" regardless of the developer's real setup
// or of what a previous test left in the package-level flag vars.
func isolatedCredentials(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHINYHUB_CREDENTIALS", path)
	t.Setenv("SHINYHUB_CONFIG", "")
	t.Setenv("SHINYHUB_HOST", "")
	t.Setenv("SHINYHUB_TOKEN", "")
	configPathOverride = ""
	hostFlagOverride = ""
	t.Cleanup(func() {
		configPathOverride = ""
		hostFlagOverride = ""
	})
	return path
}

// writeCredentialsFile drops raw JSON at the isolated credentials path, so a
// test can present a file shape this code never writes (a legacy file, a
// hand-edited one, a corrupt one) rather than only round-tripping its own
// output.
func writeCredentialsFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"already canonical", "https://shiny.example.com", "https://shiny.example.com"},
		{"trailing slash dropped", "https://shiny.example.com/", "https://shiny.example.com"},
		{"scheme and host lowercased", "HTTPS://Shiny.Example.COM", "https://shiny.example.com"},
		{"port preserved", "http://192.0.2.10:8080/", "http://192.0.2.10:8080"},
		{"query and fragment dropped", "https://shiny.example.com/?a=1#frag", "https://shiny.example.com"},
		{"surrounding space trimmed", "  https://shiny.example.com  ", "https://shiny.example.com"},
		// A reverse-proxy subpath is case-sensitive, so only the scheme and
		// authority may be folded. Lowercasing the path would point the CLI at a
		// route the proxy does not serve.
		{"path case preserved", "HTTPS://Shiny.Example.COM/ShinyHub/", "https://shiny.example.com/ShinyHub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeHost(tc.in); got != tc.want {
				t.Fatalf("normalizeHost(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Two spellings of the same server must land on one entry, or logging in with a
// trailing slash would leave a second credential that `shinyhub hosts` shows as
// a separate server.
func TestSaveConfig_SpellingVariantsShareOneEntry(t *testing.T) {
	isolatedCredentials(t)

	if err := saveConfig(&cliConfig{Host: "https://shiny.example.com", Token: "shk_first"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := saveConfig(&cliConfig{Host: "HTTPS://Shiny.Example.COM/", Token: "shk_second"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if len(st.Hosts) != 1 {
		t.Fatalf("got %d entries %v, want the two spellings folded into one", len(st.Hosts), st.sortedHosts())
	}
	if got := st.Hosts["https://shiny.example.com"].Token; got != "shk_second" {
		t.Errorf("token = %q, want the refreshed %q", got, "shk_second")
	}
}

// A file written by a pre-multi-host binary is the shape most existing installs
// have on disk. Reading it must yield a working current host, not "not logged
// in" - an upgrade that silently signs everyone out is the worst outcome here.
func TestLoadStore_ReadsLegacySingleHostFile(t *testing.T) {
	path := isolatedCredentials(t)
	writeCredentialsFile(t, path, `{"host":"https://legacy.example.com/","token":"shk_legacy"}`)

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if st.CurrentHost != "https://legacy.example.com" {
		t.Fatalf("CurrentHost = %q, want the legacy host normalized", st.CurrentHost)
	}
	cfg, err := st.resolve("", "", "")
	if err != nil {
		t.Fatalf("resolve after legacy read: %v", err)
	}
	if cfg.Host != "https://legacy.example.com" || cfg.Token != "shk_legacy" {
		t.Errorf("got %+v, want the legacy credential", cfg)
	}
}

// The file this code writes must stay readable by a binary that predates
// multi-host support: such a binary reads only the top-level host/token pair.
// This asserts the downgrade path by decoding the file the way the old code
// did, rather than by trusting that the mirror fields exist.
func TestSaveStore_RemainsReadableByPreMultiHostBinary(t *testing.T) {
	path := isolatedCredentials(t)

	if err := saveConfig(&cliConfig{Host: "https://a.example.com", Token: "shk_a"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := saveConfig(&cliConfig{Host: "https://b.example.com", Token: "shk_b"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// The exact struct the old binary decoded into.
	var old struct {
		Host  string `json:"host"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("old binary could not parse the file: %v", err)
	}
	if old.Host != "https://b.example.com" || old.Token != "shk_b" {
		t.Errorf("downgrade mirror = %+v, want the current host's credential", old)
	}
}

// Switching the current host must move the mirror with it, or a downgraded
// binary would keep talking to the server the user switched away from.
func TestSaveStore_MirrorTracksTheCurrentHost(t *testing.T) {
	path := isolatedCredentials(t)

	for _, h := range []string{"https://a.example.com", "https://b.example.com"} {
		if err := saveConfig(&cliConfig{Host: h, Token: "shk_" + h}); err != nil {
			t.Fatalf("save %s: %v", h, err)
		}
	}
	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	st.CurrentHost = "https://a.example.com"
	if err := saveStore(st); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var old struct {
		Host  string `json:"host"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if old.Host != "https://a.example.com" {
		t.Errorf("mirror host = %q, want it to follow the current host", old.Host)
	}
	if old.Token != "shk_https://a.example.com" {
		t.Errorf("mirror token = %q, want the current host's own token", old.Token)
	}
}

// The credentials file holds bearer tokens, so it must never be group- or
// world-readable, including on the temp-file path used for the atomic write.
func TestSaveStore_FileIsOwnerReadableOnly(t *testing.T) {
	path := isolatedCredentials(t)

	if err := saveConfig(&cliConfig{Host: "https://a.example.com", Token: "shk_a"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("credentials file mode = %o, want 0600", perm)
	}
}

// A file that exists but cannot be parsed is not the same fact as "no file".
// Reporting it as "not logged in" would send the user to `login`, which
// overwrites the very file whose contents they might still want to recover.
func TestLoadStore_CorruptFileIsAnErrorNotAnEmptyStore(t *testing.T) {
	path := isolatedCredentials(t)
	writeCredentialsFile(t, path, `{"hosts": {truncated`)

	st, err := loadStore()
	if err == nil {
		t.Fatalf("expected an error for a corrupt file; got store %+v", st)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should name the unreadable file, got: %v", err)
	}
}

// A missing file is the ordinary "never logged in" state and must not be an
// error - the caller decides what to do with an empty store.
func TestLoadStore_MissingFileIsAnEmptyStore(t *testing.T) {
	isolatedCredentials(t)

	st, err := loadStore()
	if err != nil {
		t.Fatalf("missing file should not be an error: %v", err)
	}
	if len(st.Hosts) != 0 || st.CurrentHost != "" {
		t.Fatalf("got %+v, want an empty store", st)
	}
}

// A hand-edited file naming no current host is recoverable when there is only
// one entry: there is nothing to disambiguate.
func TestLoadStore_SingleHostBecomesCurrentWhenNoneNamed(t *testing.T) {
	path := isolatedCredentials(t)
	writeCredentialsFile(t, path, `{"hosts":{"https://only.example.com":{"token":"shk_only"}}}`)

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if st.CurrentHost != "https://only.example.com" {
		t.Fatalf("CurrentHost = %q, want the sole entry", st.CurrentHost)
	}
}

// With several entries and no current host, picking one would be a guess about
// where the user's next command should go. The store leaves it unset and the
// resolver asks, naming the command that fixes it.
func TestResolve_AmbiguousCurrentHostAsksRatherThanGuesses(t *testing.T) {
	path := isolatedCredentials(t)
	writeCredentialsFile(t, path, `{"hosts":{
		"https://a.example.com":{"token":"shk_a"},
		"https://b.example.com":{"token":"shk_b"}
	}}`)

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if st.CurrentHost != "" {
		t.Fatalf("CurrentHost = %q, want it left unset when the file names none", st.CurrentHost)
	}
	cfg, err := st.resolve("", "", "")
	if err == nil {
		t.Fatalf("expected an error; got %+v", cfg)
	}
	if kind, code := classify(err); kind != KindAuth || code != 3 {
		t.Errorf("classify = (%q,%d), want (%q,3)", kind, code, KindAuth)
	}
	hint := hintOf(err)
	if !strings.Contains(hint, "shinyhub use") {
		t.Errorf("hint should name `shinyhub use`, got %q", hint)
	}
	for _, host := range []string{"https://a.example.com", "https://b.example.com"} {
		if !strings.Contains(hint, host) {
			t.Errorf("hint should list %s so the user can pick, got %q", host, hint)
		}
	}
}

// An unparseable legacy mirror must not resurrect an entry that is not there;
// the mirror is a fallback, never authoritative over the hosts map.
func TestLoadStore_HostsMapWinsOverStaleMirror(t *testing.T) {
	path := isolatedCredentials(t)
	writeCredentialsFile(t, path, `{
		"host":"https://stale.example.com","token":"shk_stale",
		"current_host":"https://real.example.com",
		"hosts":{"https://real.example.com":{"token":"shk_real"}}
	}`)

	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if st.CurrentHost != "https://real.example.com" {
		t.Fatalf("CurrentHost = %q, want the value from current_host", st.CurrentHost)
	}
	if _, ok := st.Hosts["https://stale.example.com"]; ok {
		t.Error("stale mirror must not become an entry when hosts is populated")
	}
	cfg, err := st.resolve("", "", "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Token != "shk_real" {
		t.Errorf("token = %q, want the hosts-map value", cfg.Token)
	}
}

func TestResolveSelector(t *testing.T) {
	isolatedCredentials(t)
	st := &credentialStore{Hosts: map[string]hostCredential{
		"https://prod.example.com":  {Name: "prod", Token: "shk_prod"},
		"https://local.example.com": {Token: "shk_local"},
	}}

	t.Run("name resolves to its host", func(t *testing.T) {
		got, err := st.resolveSelector("prod")
		if err != nil {
			t.Fatalf("resolveSelector: %v", err)
		}
		if got != "https://prod.example.com" {
			t.Fatalf("got %q, want the named host", got)
		}
	})

	t.Run("url is normalized whether or not it is saved", func(t *testing.T) {
		got, err := st.resolveSelector("HTTPS://New.Example.COM/")
		if err != nil {
			t.Fatalf("resolveSelector: %v", err)
		}
		if got != "https://new.example.com" {
			t.Fatalf("got %q, want the normalized URL", got)
		}
	})

	t.Run("unknown name lists what is saved", func(t *testing.T) {
		_, err := st.resolveSelector("staging")
		if err == nil {
			t.Fatal("expected an error for an unsaved name")
		}
		if kind, code := classify(err); kind != KindValidation || code != 1 {
			t.Errorf("classify = (%q,%d), want (%q,1)", kind, code, KindValidation)
		}
		if hint := hintOf(err); !strings.Contains(hint, "prod") {
			t.Errorf("hint should list the saved names, got %q", hint)
		}
	})

	// A bare hostname is the most common way to get this wrong, and listing
	// saved names does not explain it. The hint has to say the scheme is what
	// is missing.
	t.Run("schemeless url says the scheme is missing", func(t *testing.T) {
		_, err := st.resolveSelector("prod.example.com")
		if err == nil {
			t.Fatal("expected an error for a schemeless URL")
		}
		if hint := hintOf(err); !strings.Contains(hint, "http://") {
			t.Errorf("hint should name the missing scheme, got %q", hint)
		}
	})
}

// Selection order: --host beats SHINYHUB_HOST beats the saved current host, and
// each one brings its OWN token.
func TestResolve_SelectionOrderCarriesEachHostsOwnToken(t *testing.T) {
	isolatedCredentials(t)
	st := &credentialStore{
		CurrentHost: "https://current.example.com",
		Hosts: map[string]hostCredential{
			"https://current.example.com": {Token: "shk_current"},
			"https://env.example.com":     {Token: "shk_env"},
			"https://flag.example.com":    {Name: "flagged", Token: "shk_flag"},
		},
	}

	cases := []struct {
		name      string
		hostFlag  string
		envHost   string
		wantHost  string
		wantToken string
	}{
		{"current host when nothing overrides", "", "", "https://current.example.com", "shk_current"},
		{"env host overrides current", "", "https://env.example.com", "https://env.example.com", "shk_env"},
		{"flag overrides env", "https://flag.example.com", "https://env.example.com", "https://flag.example.com", "shk_flag"},
		{"flag accepts a saved name", "flagged", "https://env.example.com", "https://flag.example.com", "shk_flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := st.resolve(tc.hostFlag, tc.envHost, "")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if cfg.Host != tc.wantHost || cfg.Token != tc.wantToken {
				t.Fatalf("got %+v, want host=%q token=%q", cfg, tc.wantHost, tc.wantToken)
			}
		})
	}
}

// Reusing one server's credential against another URL stays possible - the same
// server behind two addresses is a real setup - but it must be said out loud.
// SHINYHUB_TOKEN is that opt-in.
func TestResolve_EnvTokenIsTheExplicitCrossHostOptIn(t *testing.T) {
	isolatedCredentials(t)
	st := &credentialStore{
		CurrentHost: "https://saved.example.com",
		Hosts:       map[string]hostCredential{"https://saved.example.com": {Token: "shk_saved"}},
	}

	// Without it: refused, and the message names both ways forward.
	if _, err := st.resolve("https://other.example.com", "", ""); err == nil {
		t.Fatal("expected a refusal when targeting a host with no saved credential")
	} else {
		hint := hintOf(err)
		if !strings.Contains(hint, "shinyhub login --host https://other.example.com") {
			t.Errorf("hint should offer logging in to the target, got %q", hint)
		}
		if !strings.Contains(hint, "SHINYHUB_TOKEN") {
			t.Errorf("hint should offer the explicit reuse path, got %q", hint)
		}
	}

	// With it: allowed, and the stored token is not the one that travels.
	cfg, err := st.resolve("https://other.example.com", "", "shk_explicit")
	if err != nil {
		t.Fatalf("resolve with SHINYHUB_TOKEN: %v", err)
	}
	if cfg.Host != "https://other.example.com" || cfg.Token != "shk_explicit" {
		t.Errorf("got %+v, want the explicitly supplied token against the named host", cfg)
	}
}

// A selector that resolves to something unusable as a request URL must fail
// before any request is built, naming which input was wrong.
func TestResolve_RejectsSelectorWithoutAScheme(t *testing.T) {
	isolatedCredentials(t)
	// Empty store so the selector cannot match a name and is treated as a URL.
	st := &credentialStore{Hosts: map[string]hostCredential{}}

	_, err := st.resolve("", "ftp://files.example.com", "shk_env")
	if err == nil {
		t.Fatal("expected a validation error for a non-http scheme")
	}
	if kind, code := classify(err); kind != KindValidation || code != 1 {
		t.Errorf("classify = (%q,%d), want (%q,1)", kind, code, KindValidation)
	}
	if !strings.Contains(err.Error(), "$SHINYHUB_HOST") {
		t.Errorf("error should name the input that was wrong, got: %v", err)
	}
}

// With nothing saved and nothing in the environment, the answer is "log in",
// not "pick a host".
func TestResolve_EmptyStoreSaysLogIn(t *testing.T) {
	isolatedCredentials(t)
	st := &credentialStore{Hosts: map[string]hostCredential{}}

	_, err := st.resolve("", "", "")
	if err == nil {
		t.Fatal("expected an error with an empty store")
	}
	if kind, code := classify(err); kind != KindAuth || code != 3 {
		t.Errorf("classify = (%q,%d), want (%q,3)", kind, code, KindAuth)
	}
	hint := hintOf(err)
	if !strings.Contains(hint, "shinyhub login") {
		t.Errorf("hint should name login, got %q", hint)
	}
	if strings.Contains(hint, "shinyhub use") {
		t.Errorf("hint should not offer `use` when nothing is saved, got %q", hint)
	}
}

// Re-running login without --name must not drop the alias the user chose
// earlier: the name is how they refer to the server, and losing it silently
// breaks every `shinyhub use <name>` and --host <name> they have scripted.
func TestSetCredential_KeepsExistingNameAndUserWhenNotSupplied(t *testing.T) {
	isolatedCredentials(t)
	st := &credentialStore{Hosts: map[string]hostCredential{}}

	st.setCredential("https://a.example.com", "prod", "shk_1", "alice")
	st.setCredential("https://a.example.com", "", "shk_2", "")

	cred := st.Hosts["https://a.example.com"]
	if cred.Name != "prod" {
		t.Errorf("name = %q, want it preserved as %q", cred.Name, "prod")
	}
	if cred.User != "alice" {
		t.Errorf("user = %q, want it preserved as %q", cred.User, "alice")
	}
	if cred.Token != "shk_2" {
		t.Errorf("token = %q, want the refreshed value", cred.Token)
	}
	if cred.SavedAt == "" {
		t.Error("saved_at should be stamped on every save")
	}
}

// saveStore must never write into the destination file in place: it stages the
// new contents beside it and renames, so an interrupted write cannot leave a
// truncated credentials file where a complete one used to be. A directory the
// process cannot create files in is the observable form of that property - the
// staged write has nowhere to go and fails, while an in-place write would
// happily reopen and rewrite the existing file (truncating a file needs no
// write permission on its directory).
func TestSaveStore_NeverWritesTheDestinationInPlace(t *testing.T) {
	path := isolatedCredentials(t)
	if err := saveConfig(&cliConfig{Host: "https://a.example.com", Token: "shk_a"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not block writes")
	}

	// The file check comes first and is the load-bearing one: it is what fails
	// when the implementation reopens the destination directly. Asserting only
	// on the error would report "no error" without saying what was written.
	saveErr := saveConfig(&cliConfig{Host: "https://b.example.com", Token: "shk_b"})
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after save: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("save wrote the destination in place instead of staging beside it:\nbefore %s\nafter  %s", before, after)
	}
	if saveErr == nil {
		t.Error("expected the save to fail when it cannot stage a file in the config directory")
	}
}

// A save that cannot complete must not leave a temp file behind that a later
// reader or a directory listing could mistake for real state.
func TestSaveStore_LeavesNoTempFileBehind(t *testing.T) {
	path := isolatedCredentials(t)
	if err := saveConfig(&cliConfig{Host: "https://a.example.com", Token: "shk_a"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Errorf("temp file %q left behind after a successful save", e.Name())
		}
	}
}

// loadConfig is what every command calls; this pins the whole chain (file ->
// store -> resolver) rather than only the resolver in isolation.
func TestLoadConfig_UsesHostFlagOverrideWithThatHostsToken(t *testing.T) {
	isolatedCredentials(t)
	if err := saveNamedConfig(&cliConfig{Host: "https://prod.example.com", Token: "shk_prod"}, "prod", "alice"); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := saveNamedConfig(&cliConfig{Host: "https://dev.example.com", Token: "shk_dev"}, "dev", "alice"); err != nil {
		t.Fatalf("save: %v", err)
	}

	hostFlagOverride = "prod"
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Host != "https://prod.example.com" || cfg.Token != "shk_prod" {
		t.Fatalf("got %+v, want the --host-named server and its own token", cfg)
	}

	// --host targets one command; it must not move the saved current host.
	st, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if st.CurrentHost != "https://dev.example.com" {
		t.Errorf("current host = %q, want --host to leave it at %q", st.CurrentHost, "https://dev.example.com")
	}
}

// The store must survive a directory that does not exist yet - the first login
// on a new machine.
func TestSaveStore_CreatesTheConfigDirectory(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "deep", "config", "credentials.json")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHINYHUB_CREDENTIALS", nested)
	t.Setenv("SHINYHUB_CONFIG", "")
	configPathOverride = ""

	if err := saveConfig(&cliConfig{Host: "https://a.example.com", Token: "shk_a"}); err != nil {
		t.Fatalf("save into a missing directory: %v", err)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("credentials file not created: %v", err)
	}
}

func TestHasScheme(t *testing.T) {
	cases := map[string]bool{
		"https://a.example.com": true,
		"http://192.0.2.1:8080": true,
		"a.example.com":         false,
		"ftp://a.example.com":   false,
		"":                      false,
		"prod":                  false,
	}
	for in, want := range cases {
		if got := hasScheme(in); got != want {
			t.Errorf("hasScheme(%q) = %v, want %v", in, got, want)
		}
	}
}

// The error the resolver returns for a corrupt file must survive being wrapped,
// so callers that use errors.Is/As on it keep working.
func TestLoadStore_CorruptFileErrorWraps(t *testing.T) {
	path := isolatedCredentials(t)
	writeCredentialsFile(t, path, `not json at all`)

	_, err := loadStore()
	if err == nil {
		t.Fatal("expected an error")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("error should wrap the decode failure, got %T: %v", err, err)
	}
}

// A credentials file that will not parse is something the person running the
// command can fix, so it must not be reported as an internal error - that sends
// them looking for a bug in the tool. The two causes get different advice, and
// each has to be pinned or the useful one silently becomes the generic one.
func TestLoadStore_UnreadableFileIsActionable(t *testing.T) {
	t.Run("damaged credentials file", func(t *testing.T) {
		path := isolatedCredentials(t)
		writeCredentialsFile(t, path, `{"hosts": {truncated`)

		_, err := loadStore()
		if err == nil {
			t.Fatal("expected an error")
		}
		if kind, code := classify(err); kind != KindValidation || code != 1 {
			t.Errorf("got kind=%q code=%d, want %q/1", kind, code, KindValidation)
		}
		hint := hintOf(err)
		if !strings.Contains(hint, "shinyhub login") {
			t.Errorf("hint should name the command that rewrites the file, got %q", hint)
		}
		// Deleting the file is destructive when several hosts are saved, so the
		// advice may not present it as a free action.
		if !strings.Contains(hint, "drops every saved host") {
			t.Errorf("hint should say what deleting the file costs, got %q", hint)
		}
	})

	// The likeliest wrong file is the server's shinyhub.yaml, because
	// SHINYHUB_CONFIG names the server config on the server and the client
	// credentials file on the client. Saying "the file is damaged" there would
	// be advice to corrupt a perfectly good server config.
	t.Run("wrong file entirely", func(t *testing.T) {
		path := isolatedCredentials(t)
		writeCredentialsFile(t, path, "server:\n  port: 8080\nauth:\n  secret: abc\n")

		_, err := loadStore()
		if err == nil {
			t.Fatal("expected an error")
		}
		hint := hintOf(err)
		if !strings.Contains(hint, "shinyhub.yaml") {
			t.Errorf("hint should name the file this is likely to be, got %q", hint)
		}
		if strings.Contains(hint, "damaged") {
			t.Errorf("hint should not tell the user to repair a file that is simply the wrong one, got %q", hint)
		}
	})
}
