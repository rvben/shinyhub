package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
)

// dataPushStallTimeout is how long an upload may go without progress (no bytes
// read from the local file, or no response headers after the body is fully
// sent) before it is aborted. It is a stall timeout, not a total-request
// deadline: a large upload that keeps making progress can run indefinitely.
const dataPushStallTimeout = 2 * time.Minute

// newDataCmd builds a fresh data command tree each time it is called.
func newDataCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "data", Short: "Manage an app's persistent data dir"}
	cmd.AddCommand(newDataPushCmd(), newDataPullCmd(), newDataLsCmd(), newDataRmCmd())
	return cmd
}

func newDataPushCmd() *cobra.Command {
	var flags struct {
		dest    string
		restart bool
		dryRun  bool
		timeout time.Duration
	}

	pushCmd := &cobra.Command{
		Use:   "push <slug> <local-file>",
		Short: "Upload a file to an app's persistent data dir",
		Args:  cobra.ExactArgs(2),
	}
	pushCmd.Flags().StringVar(&flags.dest, "dest", "", "Destination path inside the data dir (default: basename of local-file)")
	pushCmd.Flags().BoolVar(&flags.restart, "restart", false, "Restart the app after upload")
	pushCmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Resolve and print the destination and size without uploading")
	pushCmd.Flags().DurationVar(&flags.timeout, "timeout", dataPushStallTimeout,
		"Abort the upload if it makes no progress for this long (a slow but progressing upload never hits this)")
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

		if err := runDataPush(cfg.Host, cfg.Token, slug, localFile, dest, flags.restart, flags.timeout); err != nil {
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

// newDataPullCmd is the read half of `data push`. Its reason for existing is
// restore verification: after a recovery, `data ls` proves a file's name and
// size came back, and only the bytes prove the file did. Everything about the
// command is shaped by that job, which is why it reports a sha256 rather than
// just writing the file and exiting quietly.
func newDataPullCmd() *cobra.Command {
	var flags struct {
		dest  string
		force bool
	}

	pullCmd := &cobra.Command{
		Use:     "pull <slug> <remote-path>",
		Short:   "Download a file from an app's persistent data dir",
		Aliases: []string{"cat", "get"},
		Args:    cobra.ExactArgs(2),
		Long: `Download a file from an app's persistent data dir.

The file is written to the basename of <remote-path> in the current directory
unless --dest names somewhere else. "--dest -" streams the bytes to stdout,
which is the form to pipe into a checksum or a viewer.

The reported sha256 is computed from the bytes as they arrive, so it verifies
the transfer as well as the stored file: compare it against the digest recorded
when the file was pushed to confirm a restore is intact.`,
	}
	// No "-o" shorthand: that belongs to the global --output format flag, and
	// shadowing it here would turn "-o json" into a request to write a file
	// named json. --dest also mirrors "data push --dest": in both directions it
	// names where the transfer lands.
	pullCmd.Flags().StringVar(&flags.dest, "dest", "",
		`Write to this path instead of ./<basename>; "-" streams to stdout`)
	pullCmd.Flags().BoolVar(&flags.force, "force", false, "Overwrite the local destination if it already exists")
	pullCmd.RunE = func(cmd *cobra.Command, args []string) error {
		slug, remotePath := args[0], args[1]

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		dest := flags.dest
		if dest == "" {
			dest = filepath.Base(remotePath)
			// A remote path that reduces to nothing usable would otherwise write
			// to "." or "/". Refuse rather than guess a filename.
			if dest == "." || dest == "/" || dest == "" {
				return validationErr(
					fmt.Sprintf("cannot derive a local filename from remote path %q", remotePath),
					"pass --dest <local-file> to name the destination explicitly")
			}
		}
		toStdout := dest == "-"

		if !toStdout && !flags.force {
			if _, statErr := os.Stat(dest); statErr == nil {
				return validationErr(
					fmt.Sprintf("%s already exists", dest),
					"pass --force to overwrite it, or --dest <path> to write somewhere else")
			}
		}

		size, sum, err := runDataPull(cfg.Host, cfg.Token, slug, remotePath, dest, cmd.OutOrStdout())
		if err != nil {
			return err
		}

		// When the payload owns stdout the summary cannot go there too: it would
		// be appended to the file the caller is piping into a checksum. Send it
		// to stderr, where a human still reads it and a pipeline ignores it.
		out := cmd.OutOrStdout()
		local := dest
		if toStdout {
			out = cmd.ErrOrStderr()
			local = "-"
		}
		format, err := resolveFormat(false, false)
		if err != nil {
			return err
		}
		return renderActionTo(out, format, "downloaded",
			map[string]any{"slug": slug, "path": remotePath, "local": local, "bytes": size, "sha256": sum},
			fmt.Sprintf("Downloaded %s -> %s (%s, sha256 %s)", remotePath, local, humanBytes(size), sum))
	}
	return pullCmd
}

// runDataPull streams the file to dest, returning the byte count and the sha256
// of what arrived. dest "-" writes to stdout.
//
// A file destination is written through a temporary file in the same directory
// and renamed on success, so an interrupted transfer cannot leave a truncated
// file sitting where a complete one is expected - which, for a command whose
// whole job is verifying a restore, would be the worst possible failure mode.
func runDataPull(host, token, slug, remotePath, dest string, stdout io.Writer) (int64, string, error) {
	rawURL := host + "/api/apps/" + slug + "/data/" + encodeDataPath(remotePath)

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return 0, "", httpError(token, "pull data", resp, body)
	}

	h := sha256.New()

	if dest == "-" {
		n, err := io.Copy(io.MultiWriter(stdout, h), resp.Body)
		if err != nil {
			return 0, "", fmt.Errorf("download: %w", err)
		}
		return n, hex.EncodeToString(h.Sum(nil)), nil
	}

	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, ".shinyhub-pull-*")
	if err != nil {
		return 0, "", fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Removed unconditionally: after a successful rename the name no longer
	// exists and the ENOENT is discarded, and on every failure path this is what
	// stops a half-written file from being left behind.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("download: %w", err)
	}
	// Close before renaming so the rename cannot beat the last buffered write.
	if err := tmp.Close(); err != nil {
		return 0, "", fmt.Errorf("write %s: %w", dest, err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return 0, "", fmt.Errorf("write %s: %w", dest, err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func newDataLsCmd() *cobra.Command {
	f := &listFlags{}
	lsCmd := &cobra.Command{
		Use:     "ls <slug>",
		Short:   "List files in an app's persistent data dir",
		Aliases: []string{"list"},
		Args:    cobra.ExactArgs(1),
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
		// used_bytes is what the quota is charged against: deployment bundles plus
		// this data dir. data_bytes is the part these listed files add up to.
		// Printing only the first leaves an operator unable to reconcile a "Used"
		// figure in gigabytes against a listing of a few megabytes, and reading it
		// as the data dir's size sends them deleting data to reclaim space that
		// old deployments are holding.
		usedBytes, _ := extra["used_bytes"].(float64)
		dataBytes, hasDataBytes := extra["data_bytes"].(float64)

		return renderServerList(cmd, f, files, total, extra, func(w io.Writer, items []map[string]any) {
			t := newTable("PATH", "SIZE", "MODIFIED").alignRight(1)
			for _, fi := range items {
				sizeVal, _ := fi["size"].(float64)
				modVal, _ := fi["modified_at"].(float64)
				modTime := time.Unix(int64(modVal), 0).UTC().Format(time.RFC3339)
				t.row(txt(fi["path"]), txt(humanBytes(int64(sizeVal))), dimTxt(modTime))
			}
			t.render(w)
			used := humanBytes(int64(usedBytes))
			if quotaMB > 0 {
				quota := humanBytes(int64(quotaMB) * 1024 * 1024)
				fmt.Fprintf(w, "Used: %s / %s\n", used, quota)
			} else {
				fmt.Fprintf(w, "Used: %s (no quota set)\n", used)
			}
			// Only when the two differ: an equal pair adds a line that says
			// nothing, and an older server that omits data_bytes must not have a
			// zero invented for it.
			if hasDataBytes && int64(dataBytes) != int64(usedBytes) {
				fmt.Fprintf(w, "  of which this data dir: %s (the rest is deployment bundles)\n", humanBytes(int64(dataBytes)))
			}
		})
	}
	return lsCmd
}

func newDataRmCmd() *cobra.Command {
	rmCmd := &cobra.Command{
		Use:     "rm <slug> <remote-path>",
		Short:   "Remove a file from an app's persistent data dir",
		Aliases: []string{"delete"},
		Args:    cobra.ExactArgs(2),
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

// stallWatchdogReader wraps an io.Reader and cancels ctx if a read makes no
// progress for longer than timeout. Every successful read resets the clock, so
// a large upload that keeps moving bytes never trips it; a dead connection
// that stops delivering bytes to the request body does, within one timeout
// window rather than hanging until the process is killed.
type stallWatchdogReader struct {
	r       io.Reader
	timeout time.Duration
	timer   *time.Timer
	cancel  context.CancelFunc
	stalled atomic.Bool
}

func newStallWatchdogReader(r io.Reader, timeout time.Duration, cancel context.CancelFunc) *stallWatchdogReader {
	w := &stallWatchdogReader{r: r, timeout: timeout, cancel: cancel}
	w.timer = time.AfterFunc(timeout, func() {
		w.stalled.Store(true)
		cancel()
	})
	return w
}

func (w *stallWatchdogReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.timer.Reset(w.timeout)
	}
	return n, err
}

// stop disarms the watchdog once the upload has finished (successfully or
// not), so it cannot fire after the caller has moved on.
func (w *stallWatchdogReader) stop() {
	w.timer.Stop()
}

// runDataPush uploads localFile to the app's data dir at dest.
// If dest is empty, the basename of localFile is used. The client has no total
// deadline (a legitimate large upload can run indefinitely), but the transfer
// is aborted if it makes no progress for timeout: no bytes read from the local
// file while streaming the body, or no response headers once the body is
// fully sent.
func runDataPush(host, token, slug, localFile, dest string, restart bool, timeout time.Duration) error {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watchdog := newStallWatchdogReader(f, timeout, cancel)
	defer watchdog.stop()

	req, err := http.NewRequestWithContext(ctx, "PUT", rawURL, watchdog)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(token))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = info.Size()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		if watchdog.stalled.Load() {
			return fmt.Errorf("upload: stalled for more than %s with no progress", timeout)
		}
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
