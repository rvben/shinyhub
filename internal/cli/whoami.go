package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

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
		Args:  cobra.NoArgs,
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
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "whoami", resp, body)
		}

		var me struct {
			User struct {
				Username string `json:"username"`
				Role     string `json:"role"`
			} `json:"user"`
			CanCreateApps bool `json:"can_create_apps"`
		}
		if err := json.Unmarshal(body, &me); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		prose := fmt.Sprintf("Username: %s\nRole:     %s\nServer:   %s",
			me.User.Username, me.User.Role, cfg.Host)
		return renderAction(cmd, "ok", map[string]any{
			"username":        me.User.Username,
			"role":            me.User.Role,
			"host":            cfg.Host,
			"can_create_apps": me.CanCreateApps,
		}, prose)
	}
	return cmd
}
