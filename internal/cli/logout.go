package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Discard saved credentials and revoke the current session",
	Long: `Logout removes the credentials file written by ` + "`shinyhub login`" + ` and asks
the server to revoke the current JWT (best-effort — local credentials are
removed even if the server cannot be reached). API-key callers have nothing
to revoke server-side; the credentials file is still removed.`,
	RunE: runLogout,
}

func runLogout(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	cfg, err := loadConfig()
	if err != nil {
		// Already logged out (or never logged in). Treat as success — logout
		// is idempotent.
		fmt.Fprintln(out, "Not logged in.")
		return nil
	}

	// Best-effort revoke: server side cleanup is desirable but should not
	// block local cleanup. Network errors and 4xx/5xx are reported as
	// warnings, not fatal failures.
	if err := revokeServerSession(cfg); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not revoke session on server: %v\n", err)
	}

	path := configPath()
	fileRemoved := false
	if err := os.Remove(path); err == nil {
		fileRemoved = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}

	if fileRemoved {
		fmt.Fprintf(out, "Logged out. Removed %s\n", path)
	} else {
		fmt.Fprintln(out, "Logged out.")
	}

	// Warn when the environment still supplies credentials. SHINYHUB_TOKEN
	// takes precedence over (and survives the removal of) the on-disk
	// config file, so without an explicit `unset` the very next command
	// would silently re-authenticate from env — making the "Logged out"
	// message a lie. This matters most for API keys (shk_ prefix), which
	// have no server-side revocation endpoint: the env-sourced key remains
	// fully valid until the user removes it from their shell.
	if envToken := os.Getenv("SHINYHUB_TOKEN"); envToken != "" {
		vars := "SHINYHUB_TOKEN"
		if os.Getenv("SHINYHUB_HOST") != "" {
			vars = "SHINYHUB_HOST and SHINYHUB_TOKEN"
		}
		fmt.Fprintf(out,
			"Note: %s still set in your environment; subsequent commands will continue to authenticate. Run `unset %s` to fully sign out.\n",
			vars, strings.ReplaceAll(vars, " and ", " "))
	}
	return nil
}

// revokeServerSession POSTs to /api/auth/logout so the server can revoke the
// caller's JWT by jti. Any 2xx response is success; non-2xx and transport
// errors are returned to the caller for warning-level reporting.
func revokeServerSession(cfg *cliConfig) error {
	req, err := http.NewRequest("POST", cfg.Host+"/api/auth/logout", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %s", resp.Status)
	}
	return nil
}
