package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newDataCmd builds a fresh data command tree each time it is called.
func newDataCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "data", Short: "Manage an app's persistent data dir"}
	cmd.AddCommand(newDataPushCmd(), newDataLsCmd(), newDataRmCmd())
	return cmd
}

func newDataPushCmd() *cobra.Command {
	var flags struct {
		dest    string
		restart bool
		dryRun  bool
	}

	pushCmd := &cobra.Command{
		Use:   "push <slug> <local-file>",
		Short: "Upload a file to an app's persistent data dir",
		Args:  cobra.ExactArgs(2),
	}
	pushCmd.Flags().StringVar(&flags.dest, "dest", "", "Destination path inside the data dir (default: basename of local-file)")
	pushCmd.Flags().BoolVar(&flags.restart, "restart", false, "Restart the app after upload")
	pushCmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Resolve and print the destination and size without uploading")
	pushCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		localFile := args[1]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		dest := flags.dest
		if dest == "" {
			dest = filepath.Base(localFile)
		}

		// Resolve size up front so both the dry-run preview and the success line
		// can name the effective destination and byte count - the local filename
		// silently becoming the remote name is an easy footgun.
		info, err := os.Stat(localFile)
		if err != nil {
			return fmt.Errorf("open file: %w", err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory; data push uploads a single file (zip it or push files individually)", localFile)
		}
		size := info.Size()

		if flags.dryRun {
			return renderAction(cmd, "planned",
				map[string]any{"slug": slug, "path": dest, "local": localFile, "bytes": size, "dry_run": true},
				dataPushSummary(localFile, dest, size, true))
		}

		if err := runDataPush(cfg.Host, cfg.Token, slug, localFile, dest, flags.restart); err != nil {
			return err
		}
		return renderAction(cmd, "uploaded",
			map[string]any{"slug": slug, "path": dest, "local": localFile, "bytes": size},
			dataPushSummary(localFile, dest, size, false))
	}
	return pushCmd
}

// dataPushSummary builds the human-facing line for a data push, naming the
// source, the effective destination, and the size so a destination mismatch is
// impossible to miss. dryRun phrases it as a preview that uploads nothing.
func dataPushSummary(local, dest string, size int64, dryRun bool) string {
	if dryRun {
		return fmt.Sprintf("Would upload %s -> %s (%s) [dry-run, nothing uploaded]", local, dest, humanBytes(size))
	}
	return fmt.Sprintf("Uploaded %s -> %s (%s)", local, dest, humanBytes(size))
}

func newDataLsCmd() *cobra.Command {
	f := &listFlags{}
	lsCmd := &cobra.Command{
		Use:   "ls <slug>",
		Short: "List files in an app's persistent data dir",
		Args:  cobra.ExactArgs(1),
	}
	addListFlags(lsCmd, f)
	lsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		// The server paginates files server-side and carries quota metadata as
		// sibling envelope keys (quota_mb, used_bytes).
		files, total, extra, err := getPaginatedListWithExtra(cfg, "list data", "/api/apps/"+slug+"/data", f)
		if err != nil {
			return err
		}
		quotaMB, _ := extra["quota_mb"].(float64)
		usedBytes, _ := extra["used_bytes"].(float64)

		return renderServerList(cmd, f, files, total, extra, func(w io.Writer, items []map[string]any) {
			fmt.Fprintf(w, "%-48s %6s  %s\n", "PATH", "SIZE", "MODIFIED")
			for _, fi := range items {
				path := fmt.Sprintf("%v", fi["path"])
				sizeVal, _ := fi["size"].(float64)
				modVal, _ := fi["modified_at"].(float64)
				modTime := time.Unix(int64(modVal), 0).UTC().Format(time.RFC3339)
				fmt.Fprintf(w, "%-48s %6s  %s\n", path, humanBytes(int64(sizeVal)), modTime)
			}
			used := humanBytes(int64(usedBytes))
			if quotaMB > 0 {
				quota := humanBytes(int64(quotaMB) * 1024 * 1024)
				fmt.Fprintf(w, "Used: %s / %s\n", used, quota)
			} else {
				fmt.Fprintf(w, "Used: %s (no quota set)\n", used)
			}
		})
	}
	return lsCmd
}

func newDataRmCmd() *cobra.Command {
	rmCmd := &cobra.Command{
		Use:   "rm <slug> <remote-path>",
		Short: "Remove a file from an app's persistent data dir",
		Args:  cobra.ExactArgs(2),
	}
	rmCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]
		dest := args[1]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		if err := runDataRm(cfg.Host, cfg.Token, slug, dest); err != nil {
			return err
		}
		return renderAction(cmd, "removed",
			map[string]any{"slug": slug, "path": dest},
			fmt.Sprintf("%s: removed %s", slug, dest))
	}
	return rmCmd
}

// encodeDataPath splits dest by "/" and percent-encodes each segment separately,
// preserving the path structure while encoding special characters within segments.
func encodeDataPath(dest string) string {
	parts := strings.Split(dest, "/")
	encoded := make([]string, len(parts))
	for i, p := range parts {
		encoded[i] = url.PathEscape(p)
	}
	return strings.Join(encoded, "/")
}

// runDataPush uploads localFile to the app's data dir at dest.
// If dest is empty, the basename of localFile is used.
// A per-call HTTP client with no timeout is used to support large uploads.
func runDataPush(host, token, slug, localFile, dest string, restart bool) error {
	if dest == "" {
		dest = filepath.Base(localFile)
	}

	f, err := os.Open(localFile)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	encodedPath := encodeDataPath(dest)
	rawURL := host + "/api/apps/" + slug + "/data/" + encodedPath
	rawURL += fmt.Sprintf("?restart=%v", restart)

	req, err := http.NewRequest("PUT", rawURL, f)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = info.Size()

	// Use a no-timeout client for potentially large file uploads.
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		var qe struct {
			QuotaBytes     int64
			UsedBytes      int64
			WouldBeBytes   int64
			RemainingBytes int64
		}
		if err := json.Unmarshal(body, &qe); err == nil && qe.QuotaBytes > 0 {
			return fmt.Errorf("quota exceeded: would use %s of %s quota (%s remaining)",
				humanBytes(qe.WouldBeBytes),
				humanBytes(qe.QuotaBytes),
				humanBytes(qe.RemainingBytes),
			)
		}
		return fmt.Errorf("quota exceeded (HTTP 413): %s", body)
	}

	if resp.StatusCode >= 400 {
		return httpError(token, "push data", resp, body)
	}

	return nil
}

// runDataRm deletes a file from an app's data dir.
func runDataRm(host, token, slug, dest string) error {
	encodedPath := encodeDataPath(dest)
	rawURL := host + "/api/apps/" + slug + "/data/" + encodedPath

	req, err := http.NewRequest("DELETE", rawURL, nil)
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
		return httpError(token, "remove data", resp, out)
	}

	return nil
}

// humanBytes formats b as a human-readable string using IEC binary units.
// Values below 1024 use "B"; above that KiB, MiB, GiB with one decimal place.
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	n := b / unit
	for n >= unit {
		div *= unit
		exp++
		n /= unit
	}
	units := []string{"KiB", "MiB", "GiB"}
	return fmt.Sprintf("%.1f%s", float64(b)/float64(div), units[exp])
}
