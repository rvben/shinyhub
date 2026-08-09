package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// hostCredential is one saved server: the token plus enough context to say who
// the login belongs to without a network round-trip, so `shinyhub hosts` can
// answer offline.
type hostCredential struct {
	Name    string `json:"name,omitempty"`
	Token   string `json:"token"`
	User    string `json:"user,omitempty"`
	SavedAt string `json:"saved_at,omitempty"`
}

// credentialStore is the on-disk shape of the client credentials file. It holds
// one entry per server so switching between a local hub and production does not
// destroy the credential for the other one.
//
// Host and Token mirror the current entry. They exist only so a binary that
// predates multi-host support still finds a usable credential in a file this
// one wrote; on read they are a fallback, never authoritative over Hosts. The
// reverse direction cannot be rescued: an older binary's login rewrites the
// whole file with just those two fields, and the other entries are gone. That
// is why the mirror is written rather than the file being versioned - a file an
// old binary silently truncates is worse than one it can still read.
type credentialStore struct {
	Host  string `json:"host,omitempty"`
	Token string `json:"token,omitempty"`

	CurrentHost string                    `json:"current_host,omitempty"`
	Hosts       map[string]hostCredential `json:"hosts,omitempty"`
}

// normalizeHost canonicalizes a server URL so the same server typed two ways
// resolves to one entry: scheme and authority are lowercased (both are
// case-insensitive per RFC 3986), a trailing slash is dropped, and any query or
// fragment is discarded. The path keeps its case because a reverse-proxy
// subpath is case-sensitive. A value that does not parse as a URL with an
// authority is returned trimmed rather than guessed at; login rejects it with a
// message naming the fix.
func normalizeHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.TrimRight(s, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// looksLikeURL reports whether a selector is meant as a server URL rather than
// a saved host's name. Names are forbidden from containing "://" at login, so
// this split is total.
func looksLikeURL(selector string) bool {
	return strings.Contains(selector, "://")
}

// hasScheme reports whether a host string carries an http/https scheme and an
// authority. Anything else cannot be turned into a request URL, and guessing a
// scheme would silently send credentials over a protocol the operator did not
// choose.
func hasScheme(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// loadStore reads the credentials file. A missing file is not an error: it
// yields an empty store, and the caller decides whether that is fatal. A file
// that exists but cannot be parsed IS an error - silently treating corruption
// as "logged out" would hide the difference between never having logged in and
// having lost the file's contents.
func loadStore() (*credentialStore, error) {
	st := &credentialStore{Hosts: map[string]hostCredential{}}
	path := configPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}

	var onDisk credentialStore
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		return nil, unreadableCredentialsError(path, raw, err)
	}
	st.CurrentHost = normalizeHost(onDisk.CurrentHost)
	for host, cred := range onDisk.Hosts {
		st.Hosts[normalizeHost(host)] = cred
	}
	// Legacy single-host file (or one an older binary rewrote): fold the pair
	// into the map so every other code path sees one shape.
	if len(st.Hosts) == 0 && onDisk.Host != "" {
		key := normalizeHost(onDisk.Host)
		st.Hosts[key] = hostCredential{Token: onDisk.Token}
		st.CurrentHost = key
	}
	// A hand-edited or partially written file can name no current host. Prefer
	// the legacy mirror when it points at a real entry, then fall back to the
	// only entry there is. More than one entry and no current is genuinely
	// ambiguous, so it stays unset and the caller asks for `shinyhub use`.
	if _, ok := st.Hosts[st.CurrentHost]; !ok {
		st.CurrentHost = ""
		if mirror := normalizeHost(onDisk.Host); mirror != "" {
			if _, ok := st.Hosts[mirror]; ok {
				st.CurrentHost = mirror
			}
		}
		if st.CurrentHost == "" && len(st.Hosts) == 1 {
			for host := range st.Hosts {
				st.CurrentHost = host
			}
		}
	}
	return st, nil
}

// unreadableCredentialsError explains a credentials file that will not parse.
// It is a validation failure, not an internal one: every cause is something the
// person running the command can fix, and reporting it as internal sends them
// looking for a bug in the tool instead.
//
// The two causes need different advice, and the file's first byte tells them
// apart. A file that does not even start with `{` is usually the wrong file -
// most often the server's shinyhub.yaml, because SHINYHUB_CONFIG selects the
// server config on the server and the client credentials file on the client.
// A file that does start with `{` is a real credentials file that got damaged.
func unreadableCredentialsError(path string, raw []byte, cause error) error {
	hint := fmt.Sprintf("the file is damaged; repair the JSON, or delete it and run `shinyhub login --host <url>` again (deleting it drops every saved host in %s)", path)
	if trimmed := strings.TrimSpace(string(raw)); !strings.HasPrefix(trimmed, "{") {
		hint = "this does not look like a credentials file; --config and $SHINYHUB_CREDENTIALS/$SHINYHUB_CONFIG select the CLIENT credentials file (default ~/.config/shinyhub/config.json), not the server's shinyhub.yaml"
	}
	return &ExitCodeError{Code: 1, Kind: KindValidation, Err: &hintedMsgError{
		msg:   fmt.Sprintf("parse %s: %v", path, cause),
		hint:  hint,
		cause: cause,
	}}
}

// saveStore writes the store, refreshing the legacy mirror from the current
// entry first. The write goes to a temp file in the same directory and is
// renamed into place so an interrupted write cannot leave a truncated or
// half-written credentials file where a complete one used to be.
func saveStore(st *credentialStore) error {
	path := configPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	st.Host = ""
	st.Token = ""
	if cur, ok := st.Hosts[st.CurrentHost]; ok {
		st.Host = st.CurrentHost
		st.Token = cur.Token
	}

	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// No-op once the rename succeeded; removes the temp file on any error
		// path so a failed save does not litter the config directory.
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if err := json.NewEncoder(tmp).Encode(st); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// resolveSelector maps a --host value, SHINYHUB_HOST, or a `use` argument onto
// a host key. A selector containing "://" is a URL and is normalized whether or
// not it is saved; anything else is the name of a saved host and must match one.
func (st *credentialStore) resolveSelector(selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", nil
	}
	if looksLikeURL(selector) {
		return normalizeHost(selector), nil
	}
	for host, cred := range st.Hosts {
		if cred.Name == selector {
			return host, nil
		}
	}
	hint := st.knownHostsHint()
	// A selector with a dot or a colon was almost certainly meant as a URL, so
	// say what is missing rather than only listing names the user did not type.
	if strings.ContainsAny(selector, ".:") {
		hint = "a server URL must include a scheme (http:// or https://); " + hint
	}
	return "", validationErr(fmt.Sprintf("no saved host named %q", selector), hint)
}

// resolve picks the server this command targets and returns that server's own
// credential. Selection order is --host, then SHINYHUB_HOST, then the saved
// current host.
//
// The token is looked up under the resolved host and is never carried over from
// a different one. Reusing a saved token whenever the host was overridden - the
// behaviour this replaces - sends the credential for one server to whatever
// address the override happens to name, which is a disclosure a typo or an
// inherited environment variable is enough to trigger. Wanting to reuse a
// credential is legitimate (the same server reached by two URLs), so it stays
// available; it just has to be said out loud, via SHINYHUB_TOKEN or by logging
// in to the second URL.
func (st *credentialStore) resolve(hostFlag, envHost, envToken string) (*cliConfig, error) {
	selector, from := strings.TrimSpace(hostFlag), "--host"
	if selector == "" {
		selector, from = strings.TrimSpace(envHost), "$SHINYHUB_HOST"
	}

	host := st.CurrentHost
	explicit := selector != ""
	if explicit {
		resolved, err := st.resolveSelector(selector)
		if err != nil {
			return nil, err
		}
		host = resolved
	}

	if host == "" {
		if len(st.Hosts) > 0 {
			return nil, authErr("no current host selected",
				"run `shinyhub use <name|url>` to pick one; "+st.knownHostsHint())
		}
		return nil, authErr("not logged in",
			"run `shinyhub login --host <url>` first, or set SHINYHUB_HOST and SHINYHUB_TOKEN")
	}
	if explicit && !hasScheme(host) {
		return nil, validationErr(fmt.Sprintf("%s value %q is not a usable server URL", from, selector),
			"include a scheme, e.g. https://shinyhub.example.com")
	}

	if envToken != "" {
		return &cliConfig{Host: host, Token: envToken}, nil
	}
	cred, ok := st.Hosts[host]
	if !ok || cred.Token == "" {
		return nil, authErr(fmt.Sprintf("not logged in to %s", host),
			fmt.Sprintf("run `shinyhub login --host %s`, or set SHINYHUB_TOKEN to reuse a credential you know is valid there; %s",
				host, st.knownHostsHint()))
	}
	return &cliConfig{Host: host, Token: cred.Token}, nil
}

// knownHostsHint lists what the user could have meant, so an unresolved
// selector does not send them to --help to find out what exists.
func (st *credentialStore) knownHostsHint() string {
	if len(st.Hosts) == 0 {
		return "no hosts are saved yet; run `shinyhub login --host <url>` first"
	}
	labels := make([]string, 0, len(st.Hosts))
	for host, cred := range st.Hosts {
		if cred.Name != "" {
			labels = append(labels, fmt.Sprintf("%s (%s)", cred.Name, host))
			continue
		}
		labels = append(labels, host)
	}
	sort.Strings(labels)
	return "saved hosts: " + strings.Join(labels, ", ") + "; see `shinyhub hosts`"
}

// nameOwner returns the host that already uses name, if any. Names must be
// unique or `shinyhub use <name>` would be a coin flip.
func (st *credentialStore) nameOwner(name string) (string, bool) {
	for host, cred := range st.Hosts {
		if cred.Name == name {
			return host, true
		}
	}
	return "", false
}

// sortedHosts returns the saved host keys in a stable order so list output and
// error messages do not reshuffle between runs of the same command.
func (st *credentialStore) sortedHosts() []string {
	hosts := make([]string, 0, len(st.Hosts))
	for host := range st.Hosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

// label renders a host the way messages should refer to it: by name when it has
// one, with the URL in parentheses so the reader always sees which server.
func (st *credentialStore) label(host string) string {
	if cred, ok := st.Hosts[host]; ok && cred.Name != "" {
		return fmt.Sprintf("%s (%s)", cred.Name, host)
	}
	return host
}

// timeNow is a seam so tests can pin saved_at instead of asserting on wall clock.
var timeNow = func() time.Time { return time.Now() }

// setCredential adds or refreshes a host entry and makes it current. An empty
// name leaves any existing name in place, so re-running login without --name
// does not silently drop the alias the user chose earlier.
func (st *credentialStore) setCredential(host, name, token, user string) {
	if st.Hosts == nil {
		st.Hosts = map[string]hostCredential{}
	}
	cred := st.Hosts[host]
	cred.Token = token
	if user != "" {
		cred.User = user
	}
	if name != "" {
		cred.Name = name
	}
	cred.SavedAt = timeNow().UTC().Format(time.RFC3339)
	st.Hosts[host] = cred
	st.CurrentHost = host
}
