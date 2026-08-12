package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newWhoamiCmd returns the `whoami` command: the first orientation command a
// developer reaches for after login. It reports who the saved credentials
// authenticate as and which server they target, by consulting /api/auth/me
// rather than decoding the stored token by hand.
func newWhoamiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the current login: username, role, and server",
		Long: `Whoami asks the current server who the saved credential authenticates as.

It needs the server to answer, so it reports a real role rather than a decoded
guess. To see which servers are saved without contacting any of them - the
question that matters when a server is down - use ` + "`shinyhub hosts`" + `.`,
		Args: cobra.NoArgs,
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		req, err := http.NewRequest("GET", cfg.Host+"/api/auth/me", nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))

		resp, err := httpClient.Do(req)
		if err != nil {
			// The client already names the server that could not be reached and
			// carries the remedy in its hint, which is exactly what whoami owes
			// the reader: "which server am I on" is the question it answers.
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "whoami", resp, body)
		}

		me, err := decodeRemoteIdentity(body)
		if err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		now := time.Now()
		credential := credentialLifecycleAt(me.Credential, now)
		lines := []string{
			fmt.Sprintf("Username:   %s", me.Username),
			fmt.Sprintf("Role:       %s", me.Role),
			fmt.Sprintf("Server:     %s", cfg.Host),
			fmt.Sprintf("Credential: %s", credentialSummary(credential)),
		}
		if credential.CreatedAt != nil {
			lines = append(lines, "Created:    "+credential.CreatedAt.UTC().Format(time.RFC3339))
		}
		if credential.LastUsedAt != nil {
			lines = append(lines, "Last used:  "+credential.LastUsedAt.UTC().Format(time.RFC3339))
		}
		lines = append(lines, "Expires:    "+credentialExpirySummary(credential, now))
		if credential.Status == "expiring" {
			lines = append(lines, "Action:     Refresh now with `shinyhub connect --refresh`.")
		}
		return renderAction(cmd, "ok", map[string]any{
			"username":        me.Username,
			"role":            me.Role,
			"host":            cfg.Host,
			"can_create_apps": me.CanCreateApps,
			"credential":      credential,
		}, strings.Join(lines, "\n"))
	}
	return cmd
}
