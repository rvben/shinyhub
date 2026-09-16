package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

var entitlementMutationFields = []fieldSpec{
	{Name: "status", Type: "string"}, {Name: "slug", Type: "string"},
	{Name: "entitlement", Type: "string"}, {Name: "principal", Type: "string"},
}
var entitlementListEnvelope = []fieldSpec{
	{Name: "items", Type: "array"}, {Name: "total", Type: "integer"},
	{Name: "limit", Type: "integer"}, {Name: "offset", Type: "integer"},
}

func newAppsEntitlementsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "entitlements", Short: "Manage business permissions inside an app",
		Long: "Define app-specific permissions and assign them to users or IdP groups.\n" +
			"These additive grants do not grant app access or management. Use effective\n" +
			"to inspect all sources; revoking one source leaves other grants in place."}
	var description string
	define := &cobra.Command{Use: "define <slug> <entitlement>", Short: "Define or describe an app entitlement", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			payload := map[string]string{}
			if cmd.Flags().Changed("description") {
				payload["description"] = description
			}
			return runEntitlementMutation(cmd, http.MethodPut, entitlementPath(args[0], args[1]), payload, "defined", args)
		}}
	define.Flags().StringVar(&description, "description", "", "Permission description; omit to preserve it, or pass an empty string to clear it")
	remove := &cobra.Command{Use: "delete <slug> <entitlement>", Short: "Delete an entitlement with no remaining grants", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEntitlementMutation(cmd, http.MethodDelete, entitlementPath(args[0], args[1]), nil, "deleted", args)
		}}
	cmd.AddCommand(define, remove)
	for _, group := range []bool{false, true} {
		for _, grant := range []bool{true, false} {
			name, action, method := "revoke", "revoked", http.MethodDelete
			if grant {
				name, action, method = "grant", "granted", http.MethodPost
			}
			principal := "username"
			if group {
				name, principal = "group-"+name, "group"
			}
			cmd.AddCommand(&cobra.Command{Use: name + " <slug> <entitlement> <" + principal + ">", Short: "Change one entitlement grant", Args: cobra.ExactArgs(3),
				RunE: func(cmd *cobra.Command, args []string) error {
					return runEntitlementMutation(cmd, method, entitlementPath(args[0], args[1])+"/grants", map[string]string{principal: args[2]}, action, args)
				}})
		}
	}
	cmd.AddCommand(newEntitlementListCmd(false), newEntitlementListCmd(true))
	cmd.AddCommand(&cobra.Command{Use: "effective <slug> <username>", Short: "Explain a user's effective entitlements and grant sources", Args: cobra.ExactArgs(2), RunE: runEffectiveEntitlements})
	return cmd
}

func entitlementPath(slug, name string) string {
	return "/api/apps/" + url.PathEscape(slug) + "/entitlements/" + url.PathEscape(name)
}

func runEntitlementMutation(cmd *cobra.Command, method, path string, payload any, action string, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, cfg.Host+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "manage entitlement", resp, out)
	}
	fields := map[string]any{"slug": args[0], "entitlement": args[1]}
	prose := fmt.Sprintf("%s: %s %s", args[0], action, args[1])
	if len(args) == 3 {
		fields["principal"] = args[2]
		prose += " for " + args[2]
		if action == "revoked" {
			prose += "; other grant sources may still apply (check entitlements effective)"
		}
	}
	return renderAction(cmd, action, fields, prose)
}

func newEntitlementListCmd(grants bool) *cobra.Command {
	f := &listFlags{}
	name, suffix, short := "list", "/entitlements", "List defined app entitlements"
	if grants {
		name, suffix, short = "grants", "/entitlement-grants", "List app entitlement assignments"
	}
	cmd := &cobra.Command{Use: name + " <slug>", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			items, total, err := getPaginatedList(cfg, "list entitlements", "/api/apps/"+url.PathEscape(args[0])+suffix, f)
			if err != nil {
				return err
			}
			return renderServerList(cmd, f, items, total, nil, func(w io.Writer, items []map[string]any) {
				for _, item := range items {
					if !grants {
						fmt.Fprintf(w, "%v\t%v\n", item["name"], item["description"])
						continue
					}
					principal := item["username"]
					if item["source"] == "group" {
						principal = item["group"]
					}
					fmt.Fprintf(w, "%v\t%v\t%v\n", item["entitlement"], item["source"], principal)
				}
			})
		}}
	addListFlags(cmd, f)
	return cmd
}

func runEffectiveEntitlements(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	path := "/api/apps/" + url.PathEscape(args[0]) + "/entitlements/effective?" + url.Values{"username": {args[1]}}.Encode()
	req, err := http.NewRequest(http.MethodGet, cfg.Host+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "inspect entitlements", resp, out)
	}
	var result struct {
		UserID       int64    `json:"user_id"`
		Entitlements []string `json:"entitlements"`
		Sources      []struct {
			Entitlement string `json:"entitlement"`
			Source      string `json:"source"`
			Username    string `json:"username,omitempty"`
			Group       string `json:"group,omitempty"`
			UserID      int64  `json:"user_id,omitempty"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return err
	}
	var prose strings.Builder
	fmt.Fprintf(&prose, "%s: entitlements for %s", args[0], args[1])
	if len(result.Sources) == 0 {
		prose.WriteString(" (none)")
	}
	for _, source := range result.Sources {
		principal := source.Username
		if source.Source == "group" {
			principal = source.Group
		}
		fmt.Fprintf(&prose, "\n%s\t%s\t%s", source.Entitlement, source.Source, principal)
	}
	return renderAction(cmd, "effective", map[string]any{"slug": args[0], "user_id": result.UserID, "entitlements": result.Entitlements, "sources": result.Sources}, prose.String())
}
