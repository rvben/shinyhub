package main

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/cli"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
)

var oidcRebindCmd = &cobra.Command{
	Use:   "oidc-rebind-empty",
	Short: "Recover an unused OIDC issuer/client binding (offline)",
	Long:  "Stop every ShinyHub server using this database first. Recover a mistaken first-start\nOIDC configuration using the corrected server config. Refuses if any OIDC account\nor live OIDC session exists; this command cannot migrate established identities.",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		stopped, _ := cmd.Flags().GetBool("acknowledge-servers-stopped")
		if !stopped {
			return fmt.Errorf("stop every server using this database and pass --acknowledge-servers-stopped")
		}
		cfg, err := config.Load(serverConfigPath())
		if err != nil {
			return err
		}
		o := cfg.OAuth.OIDC
		if o.IssuerURL == "" || o.ClientID == "" {
			return fmt.Errorf("configure the corrected OIDC issuer and client ID before rebinding")
		}
		dsn, err := oidcRecoveryDSN(cfg.Database.DSN)
		if err != nil {
			return err
		}
		store, err := db.Open(dsn)
		if err != nil {
			return err
		}
		defer store.Close()
		if err := store.Migrate(); err != nil {
			return err
		}
		if err := store.RebindEmptyOIDCProvider(o.IssuerURL, o.ClientID); err != nil {
			return err
		}
		store.LogAuditEvent(db.AuditEventParams{Action: "link_oidc", ResourceType: "oidc_provider", ResourceID: "1", Detail: db.AuditDetail(map[string]any{"method": "offline_empty_rebind", "issuer": o.IssuerURL, "client_id": o.ClientID})})
		database := "configured PostgreSQL database"
		if !store.IsPostgres() {
			if err := store.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&database); err != nil {
				return err
			}
		}
		return cli.RenderAction(cmd, "rebound", map[string]any{"issuer": o.IssuerURL, "client_id": o.ClientID, "database": database}, "Unused OIDC binding updated to the configured issuer and client ID")
	},
}

func init() {
	oidcRebindCmd.Flags().Bool("acknowledge-servers-stopped", false, "Confirm all servers using this database are stopped")
}

// Use serve's db.Open dispatch, but never create a fresh SQLite recovery target.
func oidcRecoveryDSN(dsn string) (string, error) {
	if db.IsPostgresDSN(dsn) {
		return dsn, nil
	}
	if strings.Contains(dsn, ":memory:") {
		return "", fmt.Errorf("OIDC recovery requires an existing durable database")
	}
	var u *url.URL
	var err error
	if strings.HasPrefix(dsn, "file:") {
		u, err = url.Parse(dsn)
	} else {
		path, query, _ := strings.Cut(dsn, "?")
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		u = &url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: query}
	}
	if err != nil {
		return "", err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", err
	}
	if q.Get("mode") == "memory" {
		return "", fmt.Errorf("OIDC recovery requires an existing durable database")
	}
	q.Set("mode", "rw")
	u.RawQuery = q.Encode()
	return u.String(), nil
}
