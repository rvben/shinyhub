package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
)

func newAnnouncementsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "announcements", Short: "Manage public platform notices (platform administrator)"}
	cmd.AddCommand(newAnnouncementsPublishCmd(), newAnnouncementsGetCmd(), newAnnouncementsListCmd(), newAnnouncementsDisableCmd())
	return cmd
}

// A write can commit even when its response is lost. Keep these failures out
// of retryable exit-3 classes so pipeline retry policies cannot duplicate notices.
func announcementWriteFailure(method string, err error) error {
	action, inspect := "publication", "announcements list"
	if method == http.MethodPatch {
		action, inspect = "withdrawal", "announcements get <id>"
	}
	return &ExitCodeError{Code: 1, Kind: KindInternal, Err: &hintedMsgError{
		msg:  fmt.Sprintf("announcement %s outcome unknown: %v", action, err),
		hint: fmt.Sprintf("%s may have succeeded; run shinyhub %s before retrying", action, inspect), cause: err,
	}}
}

// Do not retry writes: a lost POST response can otherwise create duplicate notices.
func announcementRequest(cmd *cobra.Command, cfg *cliConfig, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, cfg.Host+path, reader)
	if err != nil {
		return fmt.Errorf("build announcement request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	writing := method == http.MethodPost || method == http.MethodPatch
	resp, err := httpClient.Do(req)
	if err != nil {
		if writing {
			return announcementWriteFailure(method, err)
		}
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return httpError(cfg.Token, "manage announcement", resp, data)
	}
	if err != nil {
		err = fmt.Errorf("read announcement response: %w", err)
		if writing {
			return announcementWriteFailure(method, err)
		}
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := httpError(cfg.Token, "manage announcement", resp, data)
		if writing {
			return announcementWriteFailure(method, err)
		}
		return err
	}
	if err := json.Unmarshal(data, result); err != nil {
		if writing {
			return announcementWriteFailure(method, fmt.Errorf("decode announcement response: %w", err))
		}
		return protocolFailure(cfg, &protocolError{op: "decode announcement response", err: err})
	}
	return nil
}

func announcementPath(id string) (string, error) {
	// IDs are opaque single path segments, never selectors or titles.
	if strings.TrimSpace(id) == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
		return "", validationErr("invalid announcement ID", "use the ID returned by announcements publish or list")
	}
	return "/api/announcements/" + url.PathEscape(id), nil
}

func newAnnouncementsPublishCmd() *cobra.Command {
	var title, message, severity, details, endsAt string
	var ttl time.Duration
	var dismissible bool
	cmd := &cobra.Command{Use: "publish", Short: "Publish a notice with automatic expiry", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&title, "title", "", "Public notice title (required, up to 120 characters)")
	cmd.Flags().StringVar(&message, "message", "", "Public notice message (required, up to 600 characters)")
	cmd.Flags().StringVar(&severity, "severity", "information", "Severity: information, warning, or critical")
	cmd.Flags().StringVar(&details, "details-url", "", "Public HTTP or HTTPS details link")
	cmd.Flags().DurationVar(&ttl, "ttl", 0, "Time until expiry, for example 60m (positive; choose this or --ends-at)")
	cmd.Flags().StringVar(&endsAt, "ends-at", "", "Expiry as RFC3339 with timezone (choose this or --ttl)")
	cmd.Flags().BoolVar(&dismissible, "dismissible", true, "Allow dismissal (defaults to false for critical notices)")
	_ = cmd.MarkFlagRequired("title")
	_ = cmd.MarkFlagRequired("message")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if _, err := resolveFormat(false, false); err != nil {
			return err
		}
		if cmd.Flags().Changed("ttl") == cmd.Flags().Changed("ends-at") {
			return validationErr("choose exactly one of --ttl or --ends-at", "")
		}
		now := time.Now().UTC()
		end := now.Add(ttl)
		if cmd.Flags().Changed("ttl") && ttl <= 0 {
			return validationErr("--ttl must be positive", "")
		}
		if cmd.Flags().Changed("ends-at") {
			var err error
			end, err = time.Parse(time.RFC3339, endsAt)
			if err != nil {
				return validationErr("--ends-at must be RFC3339 with a timezone", "")
			}
		}
		if !end.After(now) {
			return validationErr("expiry must be in the future", "")
		}
		if severity == "critical" && !cmd.Flags().Changed("dismissible") {
			dismissible = false
		}
		a := db.Announcement{Title: title, Message: message, Severity: severity, DetailsURL: details, Publication: "published", Dismissible: dismissible, StartsAt: &now, EndsAt: &end}
		if err := a.Validate(); err != nil {
			return validationErr(err.Error(), "")
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		// Let the server choose publication time; expiry is calculated on the CLI clock.
		body := map[string]any{"title": a.Title, "message": a.Message, "severity": a.Severity, "details_url": a.DetailsURL, "publication": "published", "dismissible": a.Dismissible, "ends_at": end.UTC()}
		var saved map[string]any
		if err := announcementRequest(cmd, cfg, http.MethodPost, "/api/announcements", body, &saved); err != nil {
			return err
		}
		if id, ok := saved["id"].(string); !ok || id == "" {
			return announcementWriteFailure(http.MethodPost, fmt.Errorf("announcement response has no ID"))
		}
		return renderAction(cmd, "published", map[string]any{"id": saved["id"], "announcement": saved}, fmt.Sprintf("Published announcement %s (expires %s).", saved["id"], end.UTC().Format(time.RFC3339)))
	}
	return cmd
}

func newAnnouncementsGetCmd() *cobra.Command {
	return &cobra.Command{Use: "get <id>", Short: "Inspect a saved announcement", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format, err := resolveFormat(false, false)
		if err != nil {
			return err
		}
		path, err := announcementPath(args[0])
		if err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		var a map[string]any
		if err := announcementRequest(cmd, cfg, http.MethodGet, path, nil, &a); err != nil {
			return err
		}
		if a["id"] != args[0] {
			return protocolFailure(cfg, fmt.Errorf("invalid announcement record"))
		}
		if format == formatJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(a)
		}
		t := newTable("FIELD", "VALUE")
		for _, key := range []string{"id", "title", "message", "severity", "publication", "status", "revision", "dismissible", "starts_at", "ends_at", "details_url"} {
			t.row(txt(key), txt(a[key]))
		}
		t.render(cmd.OutOrStdout())
		return nil
	}}
}

func newAnnouncementsListCmd() *cobra.Command {
	f := &listFlags{}
	cmd := &cobra.Command{Use: "list", Short: "List announcement history", Args: cobra.NoArgs}
	addListFlags(cmd, f)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if _, err := resolveFormat(f.jsonOutput, false); err != nil {
			return err
		}
		if err := validateWindow(f); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		items := []map[string]any{}
		for offset := 0; ; {
			var page struct {
				Announcements []map[string]any `json:"announcements"`
				HasMore       *bool            `json:"has_more"`
			}
			path := fmt.Sprintf("/api/announcements?limit=100&offset=%d", offset)
			if err := announcementRequest(cmd, cfg, http.MethodGet, path, nil, &page); err != nil {
				return err
			}
			items = append(items, page.Announcements...)
			if page.Announcements == nil || page.HasMore == nil {
				return protocolFailure(cfg, fmt.Errorf("invalid announcement list response"))
			}
			if !*page.HasMore {
				break
			}
			if len(page.Announcements) == 0 {
				return protocolFailure(cfg, fmt.Errorf("invalid announcement pagination: empty page with has_more"))
			}
			offset += len(page.Announcements)
		}
		return renderList(cmd, f, items, nil, func(w io.Writer, rows []map[string]any) {
			if len(rows) == 0 {
				fmt.Fprintln(w, "No announcements.")
				return
			}
			t := newTable("ID", "TITLE", "SEVERITY", "STATUS", "ENDS")
			for _, a := range rows {
				t.row(txt(a["id"]), txt(a["title"]), txt(a["severity"]), txt(a["status"]), txt(a["ends_at"]))
			}
			t.render(w)
		})
	}
	return cmd
}

func newAnnouncementsDisableCmd() *cobra.Command {
	var expectedRevision int64
	cmd := &cobra.Command{Use: "disable <id>", Short: "Withdraw a notice with revision protection", Long: "Withdraw the latest saved revision. Use --expected-revision from publication to protect edits made during a release. Already disabled or archived notices succeed without a write. Concurrent edits return a conflict; inspect the notice before retrying.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := resolveFormat(false, false); err != nil {
			return err
		}
		if cmd.Flags().Changed("expected-revision") && expectedRevision < 1 {
			return validationErr("--expected-revision must be positive", "")
		}
		path, err := announcementPath(args[0])
		if err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		var a db.Announcement
		if err := announcementRequest(cmd, cfg, http.MethodGet, path, nil, &a); err != nil {
			return err
		}
		if a.ID != args[0] || a.Revision < 1 || (a.Publication != "draft" && a.Publication != "published" && a.Publication != "disabled" && a.Publication != "archived") {
			return protocolFailure(cfg, fmt.Errorf("invalid announcement record"))
		}
		if a.Publication != "disabled" && a.Publication != "archived" {
			if cmd.Flags().Changed("expected-revision") && a.Revision != expectedRevision {
				return &ExitCodeError{Code: 5, Kind: KindConflict, Err: &hintedMsgError{
					msg:  fmt.Sprintf("announcement changed: expected revision %d, found %d", expectedRevision, a.Revision),
					hint: "inspect shinyhub announcements get <id> before deciding whether to withdraw the updated notice",
				}}
			}
			body := map[string]any{"expected_revision": a.Revision, "publication": "disabled"}
			var saved db.Announcement
			if err := announcementRequest(cmd, cfg, http.MethodPatch, path, body, &saved); err != nil {
				return err
			}
			if saved.ID != args[0] || saved.Publication != "disabled" || saved.Revision <= a.Revision {
				return announcementWriteFailure(http.MethodPatch, fmt.Errorf("invalid disable response"))
			}
			a = saved
		}
		return renderAction(cmd, "disabled", map[string]any{"id": args[0], "publication": a.Publication, "revision": a.Revision}, fmt.Sprintf("Announcement %s withdrawn.", args[0]))
	}}
	cmd.Flags().Int64Var(&expectedRevision, "expected-revision", 0, "Only withdraw this revision (use the revision returned by publish)")
	return cmd
}
