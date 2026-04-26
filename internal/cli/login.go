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

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a ShinyHub server",
	RunE:  runLogin,
}

var loginFlags struct {
	host     string
	token    string
	username string
	password string
}

func init() {
	loginCmd.Flags().StringVar(&loginFlags.host, "host", "", "ShinyHub server URL (e.g. https://shiny.example.com)")
	loginCmd.Flags().StringVar(&loginFlags.token, "token", "", "API token (skips username/password)")
	loginCmd.Flags().StringVar(&loginFlags.username, "username", "", "Username")
	loginCmd.Flags().StringVar(&loginFlags.password, "password", "", "Password")
	loginCmd.MarkFlagRequired("host")
}

func runLogin(cmd *cobra.Command, args []string) error {
	f := loginFlags
	if f.token != "" {
		// Verify the token is accepted by the server before persisting it.
		if err := verifyToken(f.host, f.token); err != nil {
			return fmt.Errorf("token rejected by server: %w", err)
		}
		if err := saveConfig(&cliConfig{Host: f.host, Token: f.token}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Logged in. Saved credentials to %s\n", configPath())
		return nil
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
	}

	body, _ := json.Marshal(map[string]string{"username": f.username, "password": f.password})
	resp, err := http.Post(f.host+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("login failed: %s", resp.Status)
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
	if err := saveConfig(&cliConfig{Host: f.host, Token: result.Token}); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in. Saved credentials to %s\n", configPath())
	return nil
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
// accepted by the server before it is persisted to the config file.
func verifyToken(host, token string) error {
	req, err := http.NewRequest("GET", host+"/api/auth/me", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
	}
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
	}
	return nil
}
