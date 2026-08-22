package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/spf13/cobra"
)

type connectFlags struct {
	name      string
	token     string
	tokenFile string
	username  string
	password  string
	noBrowser bool
	refresh   bool
	timeout   time.Duration
}

const defaultConnectTimeout = 5 * time.Minute

type remoteIdentity struct {
	Username           string
	Role               string
	CanCreateApps      bool
	CanCreateAppsKnown bool
	AppScope           []string
	Credential         *remoteCredential
}

func deployPermissionSummary(identity remoteIdentity) string {
	if !identity.CanCreateApps {
		return "No — ask a server administrator for developer access"
	}
	if len(identity.AppScope) > 0 {
		return "Yes — restricted to " + strings.Join(identity.AppScope, ", ")
	}
	return "Yes"
}

func deployNextStep(identity remoteIdentity) string {
	if !identity.CanCreateApps {
		return "Next: ask a ShinyHub administrator to grant developer access, then run `shinyhub whoami` to verify it."
	}
	if len(identity.AppScope) > 0 {
		return "Next: deploy one of the allowlisted apps: " + strings.Join(identity.AppScope, ", ") + "."
	}
	return "Next: run `shinyhub deploy . --open` from your app directory."
}

func addAppScope(result map[string]any, identity remoteIdentity) {
	scope := identity.AppScope
	if scope == nil {
		scope = []string{}
	}
	result["app_scope"] = scope
}

func newConnectCmd() *cobra.Command {
	f := &connectFlags{}
	cmd := &cobra.Command{
		Use:   "connect [url]",
		Short: "Connect this CLI to a ShinyHub server",
		Long: `Connect verifies a remote ShinyHub and makes it the current server. A
saved credential that still authenticates is reused without opening a browser
or rotating the key, so this command is safe to run unconditionally. When no
valid credential exists, a terminal opens the server in your browser, where you
can use any configured sign-in method—including SSO—and approve a private 90-day
CLI credential. The credential itself never passes through the browser.

For a headless machine or CI, pass --token-file or set SHINYHUB_HOST and
SHINYHUB_TOKEN. Username/password is available with --username; a missing
password is prompted for without echoing it. Without a credential or terminal,
connect fails immediately; --no-browser explicitly enables copy/paste approval
from another device.

Use --refresh to rotate the current saved credential through browser approval.
The existing credential remains untouched unless the replacement authenticates
successfully; ShinyHub then revokes the previous API key.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnect(cmd, args, f)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "Short name for this server, usable with `shinyhub use <name>`")
	cmd.Flags().StringVar(&f.token, "token", "", "API token (prefer --token-file so the secret stays out of shell history)")
	cmd.Flags().StringVar(&f.tokenFile, "token-file", "", "Read an API token from a file")
	cmd.Flags().StringVar(&f.username, "username", "", "Use local username/password login instead of browser authorization")
	cmd.Flags().StringVar(&f.password, "password", "", "Password for non-interactive local login")
	cmd.Flags().BoolVar(&f.noBrowser, "no-browser", false, "Print the authorization URL instead of opening it")
	cmd.Flags().BoolVar(&f.refresh, "refresh", false, "Rotate the saved credential through browser authorization")
	cmd.Flags().DurationVar(&f.timeout, "timeout", defaultConnectTimeout, "How long to wait for browser authorization")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string, f *connectFlags) error {
	if f.refresh && (f.token != "" || f.tokenFile != "" || f.username != "" || f.password != "") {
		return validationErr("--refresh uses browser authorization and cannot be combined with another credential source", "drop --token, --token-file, --username, and --password")
	}
	if f.refresh && f.name != "" {
		return validationErr("--name cannot be combined with --refresh", "refresh preserves the saved server name; use `shinyhub connect --name <name>` separately to rename it")
	}
	if f.token != "" && f.tokenFile != "" {
		return validationErr("--token and --token-file cannot be used together", "choose one credential source")
	}
	if f.password != "" && f.username == "" {
		return validationErr("--password requires --username", "pass --username <name>, or omit --password to use browser authorization")
	}
	if f.username != "" && (f.token != "" || f.tokenFile != "") {
		return validationErr("--username cannot be combined with a token", "choose local password login or token authentication")
	}
	if f.timeout <= 0 {
		return validationErr("--timeout must be positive", "try --timeout 5m")
	}

	st, err := loadStore()
	if err != nil {
		return err
	}
	host, err := connectTargetHost(cmd, args, st)
	if err != nil {
		return err
	}
	if err := validateHostName(st, host, f.name); err != nil {
		return err
	}
	previousCredential, hadPreviousCredential := st.Hosts[host]
	if f.refresh && (!hadPreviousCredential || previousCredential.Token == "") {
		return authErr("no saved credential to refresh for "+host, "connect first with `shinyhub connect "+host+"`")
	}

	// Resolve local credential sources before contacting the server. Explicit
	// command-line inputs outrank inherited environment variables, and both
	// outrank a saved credential. A saved credential is handled separately below
	// because a successful validation is the idempotent `current` outcome.
	token := strings.TrimSpace(f.token)
	if f.tokenFile != "" {
		b, readErr := os.ReadFile(f.tokenFile)
		if readErr != nil {
			return fmt.Errorf("read token file %s: %w", f.tokenFile, readErr)
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			return validationErr(fmt.Sprintf("token file %s is empty", f.tokenFile), "write the API token to the file without surrounding text")
		}
	}
	if token == "" && f.username == "" && !f.refresh {
		token = strings.TrimSpace(os.Getenv("SHINYHUB_TOKEN"))
	}
	useSavedCredential := token == "" && f.username == "" && !f.refresh && hadPreviousCredential && previousCredential.Token != ""
	if f.username != "" && f.password == "" && !isStdinTTY() {
		return loginMissingCredsError()
	}

	// With no possible non-interactive source, a normal connect cannot make
	// progress without either a terminal or the explicit copy/paste flow. Fail
	// before even probing the server so CI never waits on a network timeout for a
	// credential problem already knowable from local state.
	if token == "" && f.username == "" && !f.refresh && !useSavedCredential && !isStdinTTY() && !f.noBrowser {
		return connectMissingCredentialError()
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "Checking %s…\n", host)
	info, err := probeServer(&cliConfig{Host: host})
	if err != nil {
		return &ExitCodeError{Code: 6, Err: fmt.Errorf("could not verify ShinyHub at %s: %w", host, err)}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "✓ ShinyHub %s is ready%s\n", displayVersion(info.Version), runtimeSummary(info.Runtimes))
	compatibility := diagnoseCompatibility(version, info)
	if err := reportConnectCompatibility(cmd.ErrOrStderr(), compatibility); err != nil {
		return err
	}
	var previousIdentity *remoteIdentity
	if f.refresh {
		if existing, identityErr := fetchRemoteIdentity(host, previousCredential.Token); identityErr == nil {
			previousIdentity = &existing
		}
	}

	if useSavedCredential {
		identity, status, identityErr := tryRemoteIdentity(host, previousCredential.Token)
		if identityErr == nil {
			return finishCurrentConnect(cmd, st, host, f.name, identity, info)
		}
		// A 401 is the one result that means this credential is no longer
		// usable and an interactive replacement is appropriate. Rate limits,
		// authorization policy, server failures, and transport errors must stay
		// visible instead of unexpectedly minting another credential.
		if status != http.StatusUnauthorized {
			return fmt.Errorf("verify saved connection: %w", identityErr)
		}
	}

	if token == "" && f.username != "" && !f.refresh {
		password := f.password
		if password == "" {
			if !isStdinTTY() {
				return loginMissingCredsError()
			}
			password, err = promptPassword(cmd.ErrOrStderr(), "Password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
		}
		token, err = passwordLogin(host, f.username, password)
		if err != nil {
			return err
		}
	}

	if token == "" {
		// --no-browser is an explicit request for the copy/paste pairing flow.
		// Keep it usable over SSH and when a terminal multiplexer or test harness
		// captures output: neither case necessarily exposes stdin as a TTY.
		if !isStdinTTY() && !f.noBrowser {
			return connectMissingCredentialError()
		}
		if !info.Capabilities.CLIConnect {
			return authErr("this ShinyHub does not support browser CLI authorization",
				"upgrade the server, or create an API token in the dashboard and rerun with --token-file <path>")
		}
		token, err = browserAuthorizeCLI(cmd, host, f)
		if err != nil {
			return err
		}
	}

	identity, err := fetchRemoteIdentity(host, token)
	if err != nil {
		return fmt.Errorf("verify connection: %w", err)
	}
	if f.refresh && previousIdentity != nil && previousIdentity.Username != identity.Username {
		// Browser state can belong to a different account than the CLI. Do not
		// silently replace a working identity during what was requested as a
		// rotation. Best-effort cleanup keeps the just-approved key from becoming
		// an orphan while the saved credential remains untouched.
		if identity.Credential != nil && identity.Credential.Type == "api_key" && identity.Credential.ID != 0 {
			_ = revokeCredential(host, token, identity.Credential.ID)
		}
		return authErr(fmt.Sprintf("browser approved %s, but the saved credential belongs to %s", identity.Username, previousIdentity.Username),
			"sign into the browser as "+previousIdentity.Username+" and rerun `shinyhub connect --refresh`")
	}
	return finishConnect(cmd, st, host, f.name, token, identity, info, f.refresh, previousCredential.Token, previousIdentity)
}

func connectMissingCredentialError() error {
	return authErr("browser authorization requires a terminal",
		"rerun with --no-browser to copy the authorization URL, pass --token-file <path>, set SHINYHUB_HOST and SHINYHUB_TOKEN, or pass --username <name> --password <password>")
}

// offerConnectForFirstDeploy turns the most common discovery path—running
// `shinyhub deploy .` before configuring the CLI—into the onboarding flow.
// It is intentionally terminal/table-only: scripts retain the existing fast,
// structured auth error and never block on input.
func offerConnectForFirstDeploy(cmd *cobra.Command) (bool, error) {
	if !isStdinTTY() || currentFormat() != formatTable || strings.TrimSpace(os.Getenv("SHINYHUB_TOKEN")) != "" {
		return false, nil
	}
	st, err := loadStore()
	if err != nil || st.CurrentHost != "" || len(st.Hosts) != 0 {
		return false, err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "No remote ShinyHub is connected yet.")
	fmt.Fprint(cmd.ErrOrStderr(), "Connect one now? [Y/n]: ")
	line, readErr := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", readErr)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "n" || answer == "no" {
		return false, nil
	}
	if answer != "" && answer != "y" && answer != "yes" {
		return false, validationErr(fmt.Sprintf("unrecognized answer %q", answer), "answer y or n")
	}
	if err := runConnect(cmd, nil, &connectFlags{timeout: defaultConnectTimeout}); err != nil {
		return false, err
	}
	return true, nil
}

func connectTargetHost(cmd *cobra.Command, args []string, st *credentialStore) (string, error) {
	argumentHost := ""
	if len(args) == 1 {
		argumentHost = strings.TrimSpace(args[0])
	}
	flagHost := strings.TrimSpace(hostFlagOverride)
	if argumentHost != "" && flagHost != "" {
		return "", validationErr("the URL argument cannot be combined with --host", "use either `shinyhub connect <url>` or `shinyhub connect --host <name|url>`")
	}

	host := argumentHost
	if flagHost != "" {
		resolved, err := st.resolveSelector(flagHost)
		if err != nil {
			return "", err
		}
		host = resolved
	} else if host == "" {
		if envHost := strings.TrimSpace(os.Getenv("SHINYHUB_HOST")); envHost != "" {
			resolved, err := st.resolveSelector(envHost)
			if err != nil {
				return "", err
			}
			host = resolved
		}
	}
	if host == "" && st.CurrentHost != "" {
		host = st.CurrentHost
	}
	if host == "" && isStdinTTY() {
		var err error
		host, err = promptLine(cmd.InOrStdin(), cmd.ErrOrStderr(), "ShinyHub URL: ")
		if err != nil {
			return "", fmt.Errorf("read server URL: %w", err)
		}
	}
	if host == "" {
		return "", validationErr("no ShinyHub server specified", "pass a URL, e.g. `shinyhub connect https://hub.example.com`")
	}
	if !hasScheme(host) {
		return "", validationErr(fmt.Sprintf("%q is not a usable server URL", host), "include a scheme, e.g. https://hub.example.com")
	}
	return normalizeHost(host), nil
}

func passwordLogin(host, username, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := httpClient.Post(host+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", loginFailedError(resp)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if result.Token == "" {
		return "", errors.New("server returned empty token")
	}
	return result.Token, nil
}

func browserAuthorizeCLI(cmd *cobra.Command, host string, f *connectFlags) (string, error) {
	raw, err := generateLocalAPIKey()
	if err != nil {
		return "", fmt.Errorf("generate CLI credential: %w", err)
	}
	hash := auth.HashAPIKey(raw)
	name := cliCredentialName(hash)
	values := url.Values{}
	values.Set("connect_hash", hash)
	values.Set("connect_name", name)
	values.Set("connect_code", strings.ToUpper(hash[:4]+"-"+hash[4:8]))
	authorizeURL := host + "/tokens?" + values.Encode()

	fmt.Fprintln(cmd.ErrOrStderr(), "\nAuthorize this CLI in your browser:")
	fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", authorizeURL)
	if !f.noBrowser {
		if err := openBrowserURL(authorizeURL); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  Browser could not be opened automatically: %v\n", err)
		} else {
			fmt.Fprintln(cmd.ErrOrStderr(), "  Browser opened. Sign in and choose “Connect CLI”.")
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for approval (code %s)…\n", strings.ToUpper(hash[:4]+"-"+hash[4:8]))

	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, f.timeout)
	defer cancel()
	ticker := time.NewTicker(connectPollInterval)
	defer ticker.Stop()
	for {
		approved, pollErr := cliConnectionApproved(host, hash)
		if pollErr == nil && approved {
			fmt.Fprintln(cmd.ErrOrStderr(), "✓ Browser approval received")
			return raw, nil
		}
		if pollErr != nil {
			return "", fmt.Errorf("browser authorization check failed: %w", pollErr)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", authErr("browser authorization timed out", "run `shinyhub connect "+host+"` to try again")
			}
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func cliConnectionApproved(host, hash string) (bool, error) {
	u := host + "/api/auth/cli-connect/status?token_hash=" + url.QueryEscape(hash)
	resp, err := httpClient.Get(u)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, &httpStatusError{Status: resp.StatusCode,
			msg: fmt.Sprintf("pairing status (%s): %s", resp.Status, unwrapServerError(body, "no error body"))}
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decode pairing status: %w", err)
	}
	switch result.Status {
	case "pending":
		return false, nil
	case "approved":
		return true, nil
	default:
		return false, fmt.Errorf("server returned unknown pairing status %q", result.Status)
	}
}

const connectPollInterval = 2 * time.Second

func generateLocalAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "shk_" + hex.EncodeToString(b), nil
}

func cliCredentialName(hash string) string {
	hostname, _ := os.Hostname()
	hostname = strings.ToLower(hostname)
	var b strings.Builder
	for _, r := range hostname {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() >= 44 {
			break
		}
	}
	device := strings.Trim(b.String(), "-")
	if device == "" {
		device = "device"
	}
	return "cli-" + device + "-" + hash[:6]
}

func tryRemoteIdentity(host, token string) (remoteIdentity, int, error) {
	var identity remoteIdentity
	req, err := http.NewRequest(http.MethodGet, host+"/api/auth/me", nil)
	if err != nil {
		return identity, 0, err
	}
	req.Header.Set("Authorization", authHeader(token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return identity, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return identity, resp.StatusCode, &httpStatusError{Status: resp.StatusCode,
			msg: fmt.Sprintf("%s: %s", resp.Status, unwrapServerError(body, "authorization pending"))}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return identity, resp.StatusCode, fmt.Errorf("read identity: %w", err)
	}
	identity, err = decodeRemoteIdentity(body)
	if err != nil {
		return identity, resp.StatusCode, fmt.Errorf("decode identity: %w", err)
	}
	return identity, resp.StatusCode, nil
}

func fetchRemoteIdentity(host, token string) (remoteIdentity, error) {
	identity, _, err := tryRemoteIdentity(host, token)
	return identity, err
}

func finishConnect(cmd *cobra.Command, st *credentialStore, host, name, token string, identity remoteIdentity, info serverInfo, refresh bool, previousToken string, previousIdentity *remoteIdentity) error {
	previous := st.CurrentHost
	st.setCredential(host, name, token, identity.Username)
	if err := saveStore(st); err != nil {
		return err
	}
	status := "connected"
	previousRevoked := false
	var revokeWarning string
	if refresh {
		status = "refreshed"
		previousRevoked, revokeWarning = retirePreviousCredential(host, token, previousToken, previousIdentity)
		if revokeWarning != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: "+revokeWarning)
		}
	}
	saved := st.Hosts[host]
	runtimes := availableRuntimes(info.Runtimes)
	permission := deployPermissionSummary(identity)
	next := deployNextStep(identity)
	if refresh && identity.CanCreateApps && len(identity.AppScope) == 0 {
		next = "Next: run `shinyhub doctor --remote` to verify the refreshed connection."
	}
	compatibility := diagnoseCompatibility(version, info)
	verb := "Connected to"
	if refresh {
		verb = "Refreshed credential for"
	}
	prose := fmt.Sprintf("%s %s\n  Identity: %s (%s)\n  Can deploy apps: %s\n  Runtimes: %s\n  Compatibility: %s\n  Credentials: %s\n\n%s",
		verb, st.label(host), identity.Username, identity.Role, permission, strings.Join(runtimes, ", "), compatibility.Detail, configPath(), next)
	result := map[string]any{
		"host": host, "name": saved.Name, "user": identity.Username, "role": identity.Role,
		"can_create_apps": identity.CanCreateApps, "cli_version": version, "server_version": info.Version,
		"protocol_version": info.ProtocolVersion, "compatibility": compatibility.Level,
		"runtimes": runtimes, "credentials_path": configPath(), "switched_from": switchedFrom(previous, host),
		"credential": credentialLifecycleAt(identity.Credential, time.Now()),
	}
	addAppScope(result, identity)
	if refresh {
		result["previous_credential_revoked"] = previousRevoked
		result["revoke_warning"] = revokeWarning
	}
	return renderAction(cmd, status, result, prose)
}

// finishCurrentConnect reports a valid saved credential without replacing it.
// `connect` still fulfils its local selection contract: targeting another saved
// host makes it current, and --name may update its alias. When neither changes,
// the credentials file is left byte-for-byte untouched (including saved_at).
func finishCurrentConnect(cmd *cobra.Command, st *credentialStore, host, name string, identity remoteIdentity, info serverInfo) error {
	previous := st.CurrentHost
	saved := st.Hosts[host]
	changed := false
	if name != "" && saved.Name != name {
		saved.Name = name
		changed = true
	}
	if identity.Username != "" && saved.User != identity.Username {
		saved.User = identity.Username
		changed = true
	}
	if st.CurrentHost != host {
		st.CurrentHost = host
		changed = true
	}
	if changed {
		st.Hosts[host] = saved
		if err := saveStore(st); err != nil {
			return err
		}
	}

	runtimes := availableRuntimes(info.Runtimes)
	permission := deployPermissionSummary(identity)
	next := deployNextStep(identity)
	compatibility := diagnoseCompatibility(version, info)
	prose := fmt.Sprintf("Already connected to %s\n  Identity: %s (%s)\n  Can deploy apps: %s\n  Runtimes: %s\n  Compatibility: %s\n  Credentials: %s\n\n%s",
		st.label(host), identity.Username, identity.Role, permission, strings.Join(runtimes, ", "), compatibility.Detail, configPath(), next)
	result := map[string]any{
		"host": host, "name": saved.Name, "user": identity.Username, "role": identity.Role,
		"can_create_apps": identity.CanCreateApps, "cli_version": version, "server_version": info.Version,
		"protocol_version": info.ProtocolVersion, "compatibility": compatibility.Level,
		"runtimes": runtimes, "credentials_path": configPath(), "switched_from": switchedFrom(previous, host),
		"credential": credentialLifecycleAt(identity.Credential, time.Now()),
	}
	addAppScope(result, identity)
	return renderAction(cmd, "current", result, prose)
}

func revokeCredential(host, token string, id int64) error {
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/api/tokens/%d", host, id), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return httpError(token, "revoke previous credential", resp, body)
	}
	return nil
}

func retirePreviousCredential(host, newToken, previousToken string, previousIdentity *remoteIdentity) (bool, string) {
	if previousIdentity == nil || previousIdentity.Credential == nil {
		return false, "New credential saved, but this server did not identify the previous credential. Review `shinyhub tokens list` and revoke the old entry if it remains."
	}
	credential := previousIdentity.Credential
	switch credential.Type {
	case "api_key":
		if credential.ID == 0 {
			return false, "New credential saved, but this server did not report the previous API key ID. Review `shinyhub tokens list` and revoke the old entry if it remains."
		}
		if err := revokeCredential(host, newToken, credential.ID); err != nil {
			return false, fmt.Sprintf("New credential saved, but the previous API key could not be revoked automatically: %v. Revoke it with `shinyhub tokens revoke %d`.", err, credential.ID)
		}
		return true, ""
	case "session_token":
		if err := revokeSessionCredential(host, previousToken); err != nil {
			return false, fmt.Sprintf("New credential saved, but the previous session could not be revoked automatically: %v. It will remain valid until its reported expiry.", err)
		}
		return true, ""
	case "deploy_token":
		return false, "New personal credential saved. The previous deploy token is server-configured and cannot be revoked by the CLI; remove it from server configuration if it should no longer be valid."
	default:
		return false, fmt.Sprintf("New credential saved, but credential type %q cannot be revoked automatically. Review the server's credential inventory.", credential.Type)
	}
}

func revokeSessionCredential(host, token string) error {
	req, err := http.NewRequest(http.MethodPost, host+"/api/auth/logout", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return httpError(token, "revoke previous session", resp, body)
	}
	return nil
}

func switchedFrom(previous, current string) string {
	if previous != "" && previous != current {
		return previous
	}
	return ""
}

func displayVersion(v string) string {
	if strings.TrimSpace(v) == "" {
		return "server"
	}
	return v
}

func availableRuntimes(runtimes map[string]bool) []string {
	available := make([]string, 0, len(runtimes))
	for name, ok := range runtimes {
		if ok {
			available = append(available, name)
		}
	}
	sort.Strings(available)
	if len(available) == 0 {
		return []string{"none reported"}
	}
	return available
}

func runtimeSummary(runtimes map[string]bool) string {
	available := availableRuntimes(runtimes)
	if len(available) == 1 && available[0] == "none reported" {
		return " · no app runtimes reported"
	}
	return " · " + strings.Join(available, ", ")
}
