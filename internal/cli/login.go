package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// loginFlags holds the parsed flags for a single `login` invocation. It is
// constructed fresh per command instance (no package-level state) so repeated
// or shuffled test runs cannot leak flag values between each other.
type loginFlags struct {
	host     string
	name     string
	token    string
	username string
	password string
}

// newLoginCmd builds a fresh login command each time it is called, with its
// flags bound to a per-instance loginFlags value.
//
// login declares its own --host rather than inheriting the root persistent one:
// the root flag means "target this server for one command", while here the host
// is the server being signed in to and saved. Cobra resolves the local flag for
// this command, so the two never collide.
func newLoginCmd() *cobra.Command {
	f := &loginFlags{}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with a ShinyHub server",
		Long: `Login authenticates with a ShinyHub server and saves the credential under
that server's URL, keeping any other servers you are signed in to. The server
you log in to becomes the current one.

Omit --host to re-authenticate with the current server. Give --name to label a
server so you can switch to it with ` + "`shinyhub use <name>`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(cmd, f)
		},
	}
	cmd.Flags().StringVar(&f.host, "host", "", "ShinyHub server URL (e.g. https://shiny.example.com); defaults to the current host")
	cmd.Flags().StringVar(&f.name, "name", "", "Short name for this server, usable as `shinyhub use <name>`")
	cmd.Flags().StringVar(&f.token, "token", "", "API token (skips username/password)")
	cmd.Flags().StringVar(&f.username, "username", "", "Username")
	cmd.Flags().StringVar(&f.password, "password", "", "Password")
	return cmd
}

func runLogin(cmd *cobra.Command, f *loginFlags) error {
	st, err := loadStore()
	if err != nil {
		return err
	}
	host, err := loginTargetHost(st, f.host)
	if err != nil {
		return err
	}
	f.host = host
	if err := validateHostName(st, host, f.name); err != nil {
		return err
	}

	if f.token != "" {
		// Verify the token is accepted by the server before persisting it. The
		// same round-trip reports who the token belongs to, so the saved entry
		// can name its user without a second request.
		user, err := verifyToken(f.host, f.token)
		if err != nil {
			return fmt.Errorf("token rejected by server: %w", err)
		}
		return finishLogin(cmd, st, f, f.token, user)
	}

	// Prompt for missing fields when stdin is a terminal. Without this the
	// snippet `shinyhub login --host X --username Y` shown in the new-user
	// handoff modal POSTed an empty password and surfaced a confusing
	// "login failed: 401 Unauthorized" — the receiving user had no obvious
	// way to provide their password without re-reading --help. Scripts that
	// pipe credentials still work because the tty check fails and the empty
	// strings are passed through unchanged (which the server rejects with a
	// clear 401, the same as before).
	if isStdinTTY() {
		// Prompts and the password echo go to stderr so they don't pollute
		// stdout for callers like `shinyhub login --token X | jq ...`.
		// Line input is read from cmd.InOrStdin() so tests can drive the
		// flow without a real tty; the password path still goes through
		// term.ReadPassword on the real fd because it has to disable echo.
		if f.username == "" {
			u, err := promptLine(cmd.InOrStdin(), cmd.ErrOrStderr(), "Username: ")
			if err != nil {
				return fmt.Errorf("read username: %w", err)
			}
			f.username = u
		}
		if f.password == "" {
			p, err := promptPassword(cmd.ErrOrStderr(), "Password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			f.password = p
		}
	} else if f.username == "" || f.password == "" {
		// Non-TTY with missing credentials: fail fast with a structured
		// validation error that names the flags needed for non-interactive
		// login, rather than forwarding empty values to the server.
		return loginMissingCredsError()
	}

	body, _ := json.Marshal(map[string]string{"username": f.username, "password": f.password})
	resp, err := http.Post(f.host+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return loginFailedError(resp)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}
	if result.Token == "" {
		return fmt.Errorf("server returned empty token")
	}
	return finishLogin(cmd, st, f, result.Token, f.username)
}

// loginTargetHost resolves which server login signs in to: the --host value
// when given, otherwise the current host. Re-authenticating with the server you
// are already on is the common case, so requiring --host every time only forces
// the URL to be retyped; requiring it when nothing is saved is the opposite,
// because there is nothing to infer.
func loginTargetHost(st *credentialStore, hostFlag string) (string, error) {
	hostFlag = strings.TrimSpace(hostFlag)
	if hostFlag == "" {
		if st.CurrentHost == "" {
			return "", validationErr("no server to log in to",
				"pass --host <url>, e.g. --host https://shinyhub.example.com")
		}
		return st.CurrentHost, nil
	}
	if !hasScheme(hostFlag) {
		return "", validationErr(fmt.Sprintf("--host %q is not a usable server URL", hostFlag),
			"include a scheme, e.g. https://shinyhub.example.com")
	}
	return normalizeHost(hostFlag), nil
}

// validateHostName rejects a --name that could not be used as a selector or
// that already points somewhere else. A name silently rebinding to a second
// server would make `shinyhub use <name>` target whichever one was saved last.
func validateHostName(st *credentialStore, host, name string) error {
	if name == "" {
		return nil
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return validationErr(fmt.Sprintf("--name %q may not contain whitespace", name),
			"use a short single word, e.g. --name prod")
	}
	if looksLikeURL(name) {
		return validationErr(fmt.Sprintf("--name %q looks like a URL", name),
			"names must not contain `://`; a name is the short alias for a URL, e.g. --name prod")
	}
	if owner, taken := st.nameOwner(name); taken && owner != host {
		return validationErr(fmt.Sprintf("--name %q is already used by %s", name, owner),
			"choose a different name, or re-run `shinyhub login --host "+owner+" --name <other>` to rename that entry")
	}
	return nil
}

// finishLogin saves the credential under its own host and reports what changed:
// whether the server was added or its credential refreshed, and whether the
// current host moved. Naming the outcome is the point - the previous
// single-slot file replaced a different server's credential with the same
// "Logged in" message, so the one case worth noticing looked exactly like the
// routine one.
func finishLogin(cmd *cobra.Command, st *credentialStore, f *loginFlags, token, user string) error {
	_, existed := st.Hosts[f.host]
	previous := st.CurrentHost

	st.setCredential(f.host, f.name, token, user)
	if err := saveStore(st); err != nil {
		return err
	}
	saved := st.Hosts[f.host]

	status := "refreshed"
	if !existed {
		status = "added"
	}
	switchedFrom := ""
	if previous != "" && previous != f.host {
		switchedFrom = previous
	}

	var prose string
	switch {
	case switchedFrom != "":
		prose = fmt.Sprintf("Logged in to %s. Saved credentials to %s; current host was %s, other saved hosts are unchanged.",
			st.label(f.host), configPath(), switchedFrom)
	case existed:
		prose = fmt.Sprintf("Logged in to %s. Refreshed credentials in %s", st.label(f.host), configPath())
	default:
		prose = fmt.Sprintf("Logged in to %s. Saved credentials to %s", st.label(f.host), configPath())
	}

	return renderAction(cmd, status, map[string]any{
		"host":             f.host,
		"name":             saved.Name,
		"user":             saved.User,
		"current":          true,
		"switched_from":    switchedFrom,
		"credentials_path": configPath(),
	}, prose)
}

// Indirection seams so tests can stub TTY-only behaviour without faking a real
// terminal. Production code uses the real golang.org/x/term implementation;
// tests overwrite these vars to simulate stdin coming from a script vs a tty.
var (
	isStdinTTY = func() bool { return term.IsTerminal(int(syscall.Stdin)) }

	readPassword = func() (string, error) {
		b, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
)

// promptLine writes prompt to w, reads a line from r, and returns the
// trimmed value. EOF on an empty line is treated as an error so the caller
// gets a clear failure instead of POSTing an empty username.
func promptLine(r io.Reader, w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	br := bufio.NewReader(r)
	line, err := br.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("empty input")
	}
	return line, nil
}

// promptPassword writes prompt to w and reads a line from stdin without
// echoing. A trailing newline is printed afterwards because ReadPassword
// suppresses the user's own. Reads always go through the readPassword seam
// because term.ReadPassword has to operate on the real terminal fd to
// disable echo — there is no portable way to do that on a generic Reader.
func promptPassword(w io.Writer, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	pw, err := readPassword()
	fmt.Fprintln(w)
	if err != nil {
		return "", err
	}
	if pw == "" {
		return "", errors.New("empty password")
	}
	return pw, nil
}

// verifyToken does a GET /api/auth/me round-trip to confirm the token is
// accepted by the server before it is persisted to the config file, and returns
// the username it authenticates as. An unreadable or unexpected body is not an
// error: the token is verified by the status code, and the username is only
// used to label the saved entry. It comes back empty rather than guessed, so
// `shinyhub hosts` shows an unknown user as unknown.
func verifyToken(host, token string) (string, error) {
	req, err := http.NewRequest("GET", host+"/api/auth/me", nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))
	resp, err := httpClient.Do(req)
	if err != nil {
		// "connect to server:" in front of "cannot reach the ShinyHub server
		// at ..." says the same thing twice.
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", &httpStatusError{
			Status: resp.StatusCode,
			msg:    fmt.Sprintf("server returned %s: %s", resp.Status, out),
		}
	}
	var me struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	_ = json.Unmarshal(out, &me)
	return me.User.Username, nil
}
