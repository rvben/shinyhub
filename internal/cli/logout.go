package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"

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
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	fmt.Fprintf(out, "Logged out. Removed %s\n", path)
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
