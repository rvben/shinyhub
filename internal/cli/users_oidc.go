package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newUsersLinkOIDCCmd() *cobra.Command {
	var sub, issuer, clientID string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "link-oidc <username>",
		Short: "Link an existing SSO user to a verified OIDC subject (admin)",
		Long:  "Preserve the existing user ID, ownership, grants and credentials.\nObtain the exact sub from a verified ID token for this client; an Entra object ID is not sub.\nRefuses local-password, service, managed and conflicting identities. Use --dry-run to preview.",
		Args:  cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&sub, "sub", "", "Verified ID-token subject (required)")
	cmd.Flags().StringVar(&issuer, "issuer", "", "Expected configured OIDC issuer (required)")
	cmd.Flags().StringVar(&clientID, "client-id", "", "Expected configured OIDC client ID (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Check eligibility without linking")
	for _, flag := range []string{"sub", "issuer", "client-id"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		id, err := lookupUserID(cfg, args[0])
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]any{"sub": sub, "issuer": issuer, "client_id": clientID, "dry_run": dryRun})
		result, err := doAPI(cfg, apiCall{Method: "POST", Path: fmt.Sprintf("/api/users/%d/link-oidc", id), Body: body, Op: "link OIDC identity"})
		if err != nil {
			return err
		}
		var fields map[string]any
		if err := json.Unmarshal(result.Body, &fields); err != nil {
			return err
		}
		message := "OIDC identity linked to " + args[0]
		if dryRun {
			message = "OIDC identity link is eligible for " + args[0]
		}
		return renderAction(cmd, "ok", fields, message)
	}
	return cmd
}
