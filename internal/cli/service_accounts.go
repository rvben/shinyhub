package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/slug"
	"github.com/spf13/cobra"
)

func newServiceAccountsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "service-accounts", Short: "Manage non-interactive deployment identities (admin)"}
	credentials := &cobra.Command{Use: "credentials", Short: "Manage service-account credentials"}
	credentials.AddCommand(newServiceCredentialsListCmd(), newServiceCredentialsCreateCmd(), newServiceCredentialsRevokeCmd())
	cmd.AddCommand(newServiceAccountsListCmd(), credentials)
	return cmd
}

func serviceAccountRequest(cfg *cliConfig, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, cfg.Host+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, httpError(cfg.Token, "manage service account", resp, out)
	}
	return out, nil
}

func decodeItems(out []byte) ([]map[string]any, error) {
	var env struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if env.Items == nil {
		env.Items = []map[string]any{}
	}
	return env.Items, nil
}

func newServiceAccountsListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{Use: "list", Short: "List service accounts", Args: cobra.NoArgs}
	addListFlags(cmd, f)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		out, err := serviceAccountRequest(cfg, http.MethodGet, "/api/service-accounts", nil)
		if err != nil {
			return err
		}
		items, err := decodeItems(out)
		if err != nil {
			return err
		}
		return renderList(cmd, f, items, nil, func(w io.Writer, rows []map[string]any) {
			if len(rows) == 0 {
				fmt.Fprintln(w, "No service accounts.")
				return
			}
			t := newTable("KEY", "NAME", "COMPATIBILITY USERNAME", "MANAGED BY")
			for _, row := range rows {
				t.row(txt(row["key"]), txt(row["name"]), dimTxt(row["username"]), dimTxt(row["managed_by"]))
			}
			t.render(w)
		})
	}
	return cmd
}

func newServiceCredentialsListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{Use: "list <service-account>", Short: "List credentials", Args: cobra.ExactArgs(1)}
	addListFlags(cmd, f)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		out, err := serviceAccountRequest(cfg, http.MethodGet, "/api/service-accounts/"+url.PathEscape(args[0])+"/credentials", nil)
		if err != nil {
			return err
		}
		items, err := decodeItems(out)
		if err != nil {
			return err
		}
		return renderList(cmd, f, items, nil, func(w io.Writer, rows []map[string]any) {
			if len(rows) == 0 {
				fmt.Fprintln(w, "No credentials.")
				return
			}
			t := newTable("ID", "NAME", "ROLE", "APPS", "EXPIRES", "LAST USED", "MANAGED BY").alignRight(0)
			for _, row := range rows {
				apps := "all apps"
				if unrestricted, _ := row["unrestricted"].(bool); !unrestricted {
					if values, ok := row["apps"].([]any); ok {
						parts := make([]string, 0, len(values))
						for _, value := range values {
							parts = append(parts, fmt.Sprint(value))
						}
						apps = strings.Join(parts, ",")
					}
				}
				t.row(dimTxt(row["id"]), txt(row["name"]), txt(row["role"]), txt(apps),
					dimTxt(tokenTimeCell(row["expires_at"])), dimTxt(tokenTimeCell(row["last_used_at"])), dimTxt(row["managed_by"]))
			}
			t.render(w)
		})
	}
	return cmd
}

func newServiceCredentialsCreateCmd() *cobra.Command {
	var name, role string
	var apps []string
	var unrestricted bool
	var expires int
	cmd := &cobra.Command{Use: "create <service-account>", Short: "Create a scoped automation credential", Args: cobra.ExactArgs(1)}
	cmd.Flags().StringVar(&name, "name", "", "Credential name (required)")
	cmd.Flags().StringVar(&role, "role", "developer", "Effective role: viewer, developer, operator, or admin")
	cmd.Flags().StringSliceVar(&apps, "app", nil, "Allowed app slug (repeat or comma-separate)")
	cmd.Flags().BoolVar(&unrestricted, "unrestricted", false, "Allow every app (must be explicit when --app is omitted)")
	cmd.Flags().IntVar(&expires, "expires-in-days", 90, "Days until expiry (1-365)")
	_ = cmd.MarkFlagRequired("name")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateRole(role); err != nil {
			return err
		}
		if expires < 1 || expires > 365 {
			return validationErr("--expires-in-days must be between 1 and 365", "")
		}
		if unrestricted == (len(apps) > 0) {
			return validationErr("choose either one or more --app values or --unrestricted", "")
		}
		for _, app := range apps {
			if !slug.Valid(app) {
				return validationErr(fmt.Sprintf("invalid app slug %q", app), slug.HumanRule)
			}
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"name": name, "role": role, "apps": apps,
			"unrestricted": unrestricted, "expires_in_days": expires})
		out, err := serviceAccountRequest(cfg, http.MethodPost, "/api/service-accounts/"+url.PathEscape(args[0])+"/credentials", payload)
		if err != nil {
			return err
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		format, err := resolveFormat(false, false)
		if err != nil {
			return err
		}
		if format == formatJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Service credential: %v\n", result["token"])
		fmt.Fprintln(cmd.OutOrStdout(), "Store this - it will not be shown again.")
		if warning, _ := result["warning"].(string); warning != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: "+warning)
		}
		return nil
	}
	return cmd
}

func newServiceCredentialsRevokeCmd() *cobra.Command {
	return &cobra.Command{Use: "revoke <service-account> <credential-id>", Short: "Revoke a credential", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := strconv.ParseInt(args[1], 10, 64); err != nil {
				return validationErr("credential-id must be an integer", "")
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			_, err = serviceAccountRequest(cfg, http.MethodDelete, "/api/service-accounts/"+url.PathEscape(args[0])+"/credentials/"+args[1], nil)
			if err != nil {
				return err
			}
			return renderAction(cmd, "revoked", map[string]any{"service_account": args[0], "credential_id": args[1]},
				fmt.Sprintf("credential %s revoked", args[1]))
		}}
}
