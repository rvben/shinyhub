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

// dataCmd is the package-level command registered with the root command.
var dataCmd = newDataCmd()

func newDataPushCmd() *cobra.Command {
	var flags struct {
		dest    string
		restart bool
	}

	pushCmd := &cobra.Command{
		Use:   "push <slug> <local-file>",
		Short: "Upload a file to an app's persistent data dir",
		Args:  cobra.ExactArgs(2),
	}
	pushCmd.Flags().StringVar(&flags.dest, "dest", "", "Destination path inside the data dir (default: basename of local-file)")
	pushCmd.Flags().BoolVar(&flags.restart, "restart", false, "Restart the app after upload")
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

		if err := runDataPush(cfg.Host, cfg.Token, slug, localFile, dest, flags.restart); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: uploaded %s\n", slug, dest)
		return nil
	}
	return pushCmd
}

func newDataLsCmd() *cobra.Command {
	lsCmd := &cobra.Command{
		Use:   "ls <slug>",
		Short: "List files in an app's persistent data dir",
		Args:  cobra.ExactArgs(1),
	}
	lsCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug := args[0]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		out, err := runDataLs(cfg.Host, cfg.Token, slug)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), out)
		return nil
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
		fmt.Fprintf(cmd.OutOrStdout(), "%s: removed %s\n", slug, dest)
		return nil
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
		return fmt.Errorf("server returned %s: %s", resp.Status, body)
	}

	return nil
}

// runDataLs lists files in an app's data dir and returns a formatted string.
func runDataLs(host, token, slug string) (string, error) {
	req, err := http.NewRequest("GET", host+"/api/apps/"+slug+"/data", nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server returned %s: %s", resp.Status, out)
	}

	var result struct {
		Files []struct {
			Path       string `json:"path"`
			Size       int64  `json:"size"`
			SHA256     string `json:"sha256"`
			ModifiedAt int64  `json:"modified_at"`
		} `json:"files"`
		QuotaMB   int64 `json:"quota_mb"`
		UsedBytes int64 `json:"used_bytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-48s %6s  %s\n", "PATH", "SIZE", "MODIFIED")
	for _, fi := range result.Files {
		modTime := time.Unix(fi.ModifiedAt, 0).UTC().Format(time.RFC3339)
		fmt.Fprintf(&sb, "%-48s %6s  %s\n", fi.Path, humanBytes(fi.Size), modTime)
	}

	used := humanBytes(result.UsedBytes)
	if result.QuotaMB > 0 {
		quota := humanBytes(result.QuotaMB * 1024 * 1024)
		fmt.Fprintf(&sb, "Used: %s / %s\n", used, quota)
	} else {
		fmt.Fprintf(&sb, "Used: %s (no quota set)\n", used)
	}

	return sb.String(), nil
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
		return fmt.Errorf("server returned %s: %s", resp.Status, out)
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
