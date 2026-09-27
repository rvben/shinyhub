package cli

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func newCacheCmd() *cobra.Command {
	cacheCmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage an app's shared result cache",
		Long: `Manage the disk cache an app's processes share through SHINYHUB_CACHE_DIR.

Each deployment starts with an empty cache, so a new bundle never reads
results the previous code computed. Clear it by hand when the data those
results were computed from changes without a redeploy.`,
	}
	cacheCmd.AddCommand(newCacheClearCmd())
	return cacheCmd
}

func newCacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear <slug>",
		Short: "Delete everything in an app's result cache",
		Long: `Delete everything in an app's result cache.

The app must be stopped, with no scheduled run in progress: a running process
keeps its cache open, and removing it underneath the process breaks caching
until the process restarts. Stop the app, clear, then start it again.`,
		Example: `  shinyhub apps stop sales
  shinyhub cache clear sales
  shinyhub apps start sales`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if err := runCacheClear(cfg.Host, cfg.Token, slug); err != nil {
				return err
			}
			return renderAction(cmd, "cleared",
				map[string]any{"slug": slug},
				fmt.Sprintf("%s: result cache cleared", slug))
		},
	}
}

func runCacheClear(host, token, slug string) error {
	req, err := http.NewRequest(http.MethodDelete, host+"/api/apps/"+url.PathEscape(slug)+"/cache", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return httpError(token, "clear result cache", resp, out)
	}
	return nil
}
