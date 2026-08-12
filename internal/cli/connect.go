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
	"os/exec"
	"runtime"
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
	timeout   time.Duration
}

const defaultConnectTimeout = 5 * time.Minute

type remoteIdentity struct {
	Username      string
	Role          string
	CanCreateApps bool
}

func newConnectCmd() *cobra.Command {
	f := &connectFlags{}
	cmd := &cobra.Command{
		Use:   "connect [url]",
		Short: "Connect this CLI to a ShinyHub server",
		Long: `Connect verifies a remote ShinyHub, authorizes this CLI, and saves it as
the current server. In a terminal it opens the server in your browser, where
you can use any configured sign-in method—including SSO—and approve a private
90-day CLI credential. The credential itself never passes through the browser.

For a headless machine or CI, pass --token-file or set SHINYHUB_HOST and
SHINYHUB_TOKEN. Username/password is available with --username; a missing
password is prompted for without echoing it.`,
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
	cmd.Flags().DurationVar(&f.timeout, "timeout", defaultConnectTimeout, "How long to wait for browser authorization")
	return cmd
}

func runConnect(cmd *cobra.Command, args []string, f *connectFlags) error {
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
	if token == "" {
		token = strings.TrimSpace(os.Getenv("SHINYHUB_TOKEN"))
	}

	if token == "" && f.username != "" {
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
			return authErr("browser authorization requires a terminal",
				"rerun with --no-browser to copy the authorization URL, pass --token-file <path>, or set SHINYHUB_HOST and SHINYHUB_TOKEN")
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
	return finishConnect(cmd, st, host, f.name, token, identity, info)
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

var openBrowserURL = func(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	return exec.Command(command, args...).Start()
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
	var payload struct {
		User struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
		CanCreateApps bool `json:"can_create_apps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return identity, resp.StatusCode, fmt.Errorf("decode identity: %w", err)
	}
	identity = remoteIdentity{Username: payload.User.Username, Role: payload.User.Role, CanCreateApps: payload.CanCreateApps}
	return identity, resp.StatusCode, nil
}

func fetchRemoteIdentity(host, token string) (remoteIdentity, error) {
	identity, _, err := tryRemoteIdentity(host, token)
	return identity, err
}

func finishConnect(cmd *cobra.Command, st *credentialStore, host, name, token string, identity remoteIdentity, info serverInfo) error {
	previous := st.CurrentHost
	st.setCredential(host, name, token, identity.Username)
	if err := saveStore(st); err != nil {
		return err
	}
	saved := st.Hosts[host]
	runtimes := availableRuntimes(info.Runtimes)
	permission := "No — ask a server administrator for developer access"
	if identity.CanCreateApps {
		permission = "Yes"
	}
	next := "Next: run `shinyhub deploy . --wait` from your app directory."
	if !identity.CanCreateApps {
		next = "Next: ask a ShinyHub administrator to grant developer access, then run `shinyhub whoami` to verify it."
	}
	compatibility := diagnoseCompatibility(version, info)
	prose := fmt.Sprintf("Connected to %s\n  Identity: %s (%s)\n  Can deploy apps: %s\n  Runtimes: %s\n  Compatibility: %s\n  Credentials: %s\n\n%s",
		st.label(host), identity.Username, identity.Role, permission, strings.Join(runtimes, ", "), compatibility.Detail, configPath(), next)
	return renderAction(cmd, "connected", map[string]any{
		"host": host, "name": saved.Name, "user": identity.Username, "role": identity.Role,
		"can_create_apps": identity.CanCreateApps, "cli_version": version, "server_version": info.Version,
		"protocol_version": info.ProtocolVersion, "compatibility": compatibility.Level,
		"runtimes": runtimes, "credentials_path": configPath(), "switched_from": switchedFrom(previous, host),
	}, prose)
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
