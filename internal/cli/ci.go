package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newCICmd() *cobra.Command { return newCICommand(runCIChild) }

func newCICommand(run func(*cobra.Command, []string, string, string) error) *cobra.Command {
	var policy, audience, tokenFile string
	cmd := &cobra.Command{
		Use:   "ci --policy <name> --audience <audience> -- <shinyhub-command> [args...]",
		Short: "Run a ShinyHub command with a short-lived CI workload credential",
		Long:  "Exchange a GitHub Actions OIDC token (or --identity-token-file) for an app-scoped ten-minute credential. Requires an explicit HTTPS --host URL or SHINYHUB_HOST. The credential is passed only to the child process and is never saved or printed. Put the child command and its flags after --.",
		Args:  cobra.MinimumNArgs(1),
	}
	cmd.Flags().StringVar(&policy, "policy", "", "Trusted publisher policy configured by the server administrator")
	cmd.Flags().StringVar(&audience, "audience", "", "Exact audience in that policy (required for GitHub token acquisition)")
	cmd.Flags().StringVar(&tokenFile, "identity-token-file", "", "Read a CI identity token from a file, or - for stdin")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.ArgsLenAtDash() != 0 || policy == "" {
			return validationErr("ci requires --policy and a command after --", "Example: shinyhub ci --host https://hub.example --policy production --audience https://hub.example -- deploy .")
		}
		switch args[0] {
		case "deploy", "plan", "apply", "doctor", "fleet", "apps", "env", "data", "schedule", "whoami":
		default:
			return validationErr("unsupported CI command", "Use a deployment, preflight, or app-management command.")
		}
		for _, arg := range args[1:] {
			if arg == "--host" || strings.HasPrefix(arg, "--host=") {
				return validationErr("set --host before the CI command separator", "The child command must use the server that issued its credential.")
			}
		}
		host := hostFlagOverride
		if host == "" {
			host = os.Getenv("SHINYHUB_HOST")
		}
		if err := trustedHTTPSURL(host); err != nil {
			return validationErr("ci requires an explicit HTTPS server URL", "Set --host https://hub.example or SHINYHUB_HOST; saved host names are not used in CI.")
		}
		host = normalizeHost(host)
		var assertion string
		var err error
		if tokenFile != "" {
			var reader io.Reader = cmd.InOrStdin()
			if tokenFile != "-" {
				f, openErr := os.Open(tokenFile)
				if openErr != nil {
					return fmt.Errorf("open identity token file: %w", openErr)
				}
				defer f.Close()
				reader = f
			}
			b, readErr := io.ReadAll(io.LimitReader(reader, (32<<10)+1))
			if readErr != nil || len(b) > 32<<10 {
				return validationErr("could not read identity token (maximum 32 KiB)", "Provide a fresh CI identity token file.")
			}
			assertion = strings.TrimSpace(string(b))
		} else {
			if audience == "" {
				return validationErr("--audience is required for GitHub Actions", "Use the exact audience from the server policy.")
			}
			assertion, err = githubCIIdentity(cmd.Context(), audience)
			if err != nil {
				return err
			}
		}
		if assertion == "" {
			return validationErr("identity token is empty", "Request a fresh CI identity token.")
		}
		credential, err := exchangeCIIdentity(cmd.Context(), host, policy, assertion)
		if err != nil {
			return err
		}
		childArgs := []string{"--host", host}
		for _, name := range []string{"config", "output", "quiet", "no-color"} {
			if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
				childArgs = append(childArgs, "--"+name+"="+flag.Value.String())
			}
		}
		return run(cmd, append(childArgs, args...), host, credential)
	}
	return cmd
}

// The child already rendered its output, including structured errors.
type ciChildExit struct{ *ExitCodeError }

func (e *ciChildExit) Unwrap() error { return e.ExitCodeError }

func runCIChild(cmd *cobra.Command, args []string, host, credential string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.CommandContext(cmd.Context(), executable, args...)
	child.Env = ciEnvironment(os.Environ(), host, credential)
	child.Stdin, child.Stdout, child.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := child.Run(); err != nil {
		if cmd.Context().Err() != nil {
			return cmd.Context().Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return &ciChildExit{&ExitCodeError{Code: exit.ExitCode()}}
		}
		return err
	}
	return nil
}

func trustedHTTPSURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return errors.New("expected an HTTPS URL without credentials, query, or fragment")
	}
	return nil
}

func ciEnvironment(base []string, host, credential string) []string {
	result := make([]string, 0, len(base)+3)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "SHINYHUB_HOST", "SHINYHUB_TOKEN", "CI", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL":
			continue
		}
		result = append(result, entry)
	}
	return append(result, "SHINYHUB_HOST="+host, "SHINYHUB_TOKEN="+credential, "CI=1")
}

// Never follow redirects: a 307/308 must not replay the assertion POST body.
func ciHTTPClient() *http.Client {
	return &http.Client{Transport: httpClient.Transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func githubCIIdentity(ctx context.Context, audience string) (string, error) {
	rawURL, requestToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !strings.HasSuffix(u.Hostname(), ".actions.githubusercontent.com") || requestToken == "" {
		return "", validationErr("GitHub Actions OIDC is unavailable", "Grant the job id-token: write, or pass --identity-token-file for another CI provider.")
	}
	q := u.Query()
	q.Set("audience", audience)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("could not create CI identity request")
	}
	req.Header.Set("Authorization", "Bearer "+requestToken)
	resp, err := ciHTTPClient().Do(req)
	if err != nil {
		return "", errors.New("could not request GitHub Actions identity token")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub Actions identity request returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if decodeCIResponse(resp.Body, 40<<10, &payload) != nil || payload.Value == "" || len(payload.Value) > 32<<10 {
		return "", errors.New("GitHub Actions returned an invalid identity response")
	}
	return payload.Value, nil
}

func exchangeCIIdentity(ctx context.Context, host, policy, assertion string) (string, error) {
	body, _ := json.Marshal(map[string]string{"policy": policy, "identity_token": assertion})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/auth/trusted-publishing", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("invalid trusted publishing URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ciHTTPClient().Do(req)
	if err != nil {
		return "", errors.New("could not exchange CI identity; request a fresh identity token before retrying")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", &httpStatusError{Status: resp.StatusCode, msg: fmt.Sprintf("trusted publishing returned HTTP %d; check the server policy and request a fresh identity token before retrying", resp.StatusCode)}
	}
	var result struct {
		Token   string    `json:"token"`
		Type    string    `json:"token_type"`
		Expires time.Time `json:"expires_at"`
	}
	if decodeCIResponse(resp.Body, 16<<10, &result) != nil || !strings.HasPrefix(result.Token, "shk_") || strings.ContainsAny(result.Token, "\r\n\x00") || result.Type != "Token" || !result.Expires.After(time.Now()) {
		return "", errors.New("invalid trusted publishing credential response")
	}
	return result.Token, nil
}

func decodeCIResponse(reader io.Reader, limit int64, value any) error {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return errors.New("CI response exceeds size limit")
	}
	return json.Unmarshal(body, value)
}
