package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

// shareCmd is the package-level command registered with the root command.
var shareCmd = newShareCmd()

// newShareCmd builds a fresh share command tree each time it is called.
func newShareCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "share", Short: "Manage shared-data mounts between apps"}
	cmd.AddCommand(newShareLsCmd(), newShareAddCmd(), newShareRmCmd())
	return cmd
}

// sharedDataDTO mirrors the server's JSON representation of a shared-data mount.
type sharedDataDTO struct {
	SourceSlug string `json:"source_slug"`
	SourceID   int64  `json:"source_id"`
}

func newShareLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls <slug>",
		Short: "List shared-data mounts for an app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			req, err := http.NewRequest("GET", cfg.Host+"/api/apps/"+slug+"/shared-data", nil)
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
				out, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("server returned %s: %s", resp.Status, out)
			}

			var mounts []sharedDataDTO
			if err := json.NewDecoder(resp.Body).Decode(&mounts); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}

			if len(mounts) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No shared-data mounts.")
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%-32s  %s\n", "SOURCE SLUG", "SOURCE ID")
			for _, m := range mounts {
				fmt.Fprintf(cmd.OutOrStdout(), "%-32s  %d\n", m.SourceSlug, m.SourceID)
			}
			return nil
		},
	}
}

func newShareAddCmd() *cobra.Command {
	var from string

	addCmd := &cobra.Command{
		Use:   "add <slug>",
		Short: "Mount another app's data dir into an app",
		Args:  cobra.ExactArgs(1),
	}
	addCmd.Flags().StringVar(&from, "from", "", "Source app slug whose data dir to mount (required)")
	_ = addCmd.MarkFlagRequired("from")

	addCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		body, err := json.Marshal(map[string]string{"source_slug": from})
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}

		req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/shared-data", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return fmt.Errorf("server returned %s: %s", resp.Status, out)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s: mounted data from %q\n", slug, from)
		return nil
	}
	return addCmd
}

func newShareRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <slug> <source-slug>",
		Short: "Remove a shared-data mount from an app",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug, sourceSlug := args[0], args[1]

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			url := cfg.Host + "/api/apps/" + slug + "/shared-data/" + sourceSlug
			req, err := http.NewRequest("DELETE", url, nil)
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
				out, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("server returned %s: %s", resp.Status, out)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s: removed shared-data mount %q\n", slug, sourceSlug)
			return nil
		},
	}
}
