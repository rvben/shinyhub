package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"
)

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
	// Warning is set by the server on grant under the native runtime, where the
	// read-only mount is a convention only (writes through the symlink are not
	// blocked). Empty under the Docker runtime.
	Warning string `json:"warning,omitempty"`
}

func newShareLsCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{
		Use:   "ls <slug>",
		Short: "List shared-data mounts for an app",
		Args:  cobra.ExactArgs(1),
	}
	addListFlags(cmd, f)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
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

		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "list shared-data", resp, out)
		}

		var mounts []map[string]any
		if err := json.Unmarshal(out, &mounts); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return renderList(cmd, f, mounts, nil, func(w io.Writer, items []map[string]any) {
			if len(items) == 0 {
				fmt.Fprintln(w, "No shared-data mounts.")
				return
			}
			fmt.Fprintf(w, "%-32s  %s\n", "SOURCE SLUG", "SOURCE ID")
			for _, m := range items {
				sourceSlug := fmt.Sprintf("%v", m["source_slug"])
				sourceID := fmt.Sprintf("%v", m["source_id"])
				fmt.Fprintf(w, "%-32s  %s\n", sourceSlug, sourceID)
			}
		})
	}
	return cmd
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
			return httpError(cfg.Token, "add shared-data", resp, out)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "%s: mounted data from %q\n", slug, from)

		var dto sharedDataDTO
		if err := json.Unmarshal(out, &dto); err == nil && dto.Warning != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: "+dto.Warning)
		}
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
				return httpError(cfg.Token, "remove shared-data", resp, out)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s: removed shared-data mount %q\n", slug, sourceSlug)
			return nil
		},
	}
}
