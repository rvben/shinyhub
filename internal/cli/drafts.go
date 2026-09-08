package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/spf13/cobra"
)

func draftRequest(cmd *cobra.Command, cfg *cliConfig, method, path string, body io.Reader, contentType string, allowDowntime bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(cmd.Context(), method, cfg.Host+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("X-Shinyhub-Deploy-Channel", "cli")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if allowDowntime {
		req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		err := httpError(cfg.Token, "draft", resp, data)
		if preview := resp.Header.Get("X-Shinyhub-Draft-Preview"); preview != "" {
			return nil, fmt.Errorf("preview %s exists but did not start; configure its environment/data and retry preview: %w", preview, err)
		}
		return nil, err
	}
	return data, nil
}

func runCreateDraft(cmd *cobra.Command, cfg *cliConfig, slug string, plan *bundlePreview, f *deployFlags, format outputFormat) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		return err
	}
	if _, err = io.Copy(part, plan.Buffer); err != nil {
		return err
	}
	if err = mw.Close(); err != nil {
		return err
	}
	data, err := draftRequest(cmd, cfg, "POST", "/api/apps/"+slug+"/drafts?ttl="+url.QueryEscape(f.draftTTL.String()), &body, mw.FormDataContentType(), false)
	if err != nil {
		return err
	}
	var d db.DeploymentDraft
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("decode draft: %w", err)
	}
	if d.ID == "" || d.ContentDigest != plan.Digest {
		return fmt.Errorf("server did not confirm the exact draft bundle")
	}
	if f.open {
		fmt.Fprintf(cmd.ErrOrStderr(), "Draft %s retained; preparing private preview...\n", d.ID)
		data, err = draftRequest(cmd, cfg, "POST", "/api/apps/"+slug+"/drafts/"+d.ID+"/preview", nil, "", false)
		if err != nil {
			return fmt.Errorf("draft %s retained; retry with `shinyhub drafts preview %s %s`: %w", d.ID, slug, d.ID, err)
		}
		if err = json.Unmarshal(data, &d); err != nil {
			return err
		}
		if d.PreviewSlug == "" {
			return fmt.Errorf("server omitted preview slug")
		}
		openAppURL(remoteAppURL(cfg.Host, d.PreviewSlug), false, cmd.ErrOrStderr())
	}
	return printDraft(cmd, cfg, slug, d, format)
}

func printDraft(cmd *cobra.Command, cfg *cliConfig, slug string, d db.DeploymentDraft, format outputFormat) error {
	if format == formatJSON || format == formatNDJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(d)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Draft %s\nBundle: %s\nExpires: %s\n", d.ID, d.ContentDigest, time.Unix(d.ExpiresAt, 0).UTC().Format(time.RFC3339))
	if d.PreviewSlug != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Private preview: %s\nReviewer access: shinyhub apps access grant %s <username>\n", remoteAppURL(cfg.Host, d.PreviewSlug), d.PreviewSlug)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Preview: shinyhub drafts preview %s %s --open\n", slug, d.ID)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Promote after review: shinyhub drafts promote %s %s\n", slug, d.ID)
	return nil
}

func newDraftsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "drafts", Short: "Review private deployment drafts and promote retained bundles", Long: `Upload with 'shinyhub deploy . --slug <app> --draft'. The production app must
already exist. Drafts retain the exact source archive for 24 hours by default.
Preview runs that archive as a separate private, expiring app. Production
credentials and data are not copied; configure preview credentials and data
explicitly. Hooks, schedules and access-group declarations are not supported
by previews yet. Grant reviewers viewer access to the preview app.
Promotion uses the retained archive and refuses a changed production baseline.
It rebuilds dependencies and preserves the normal no-downtime deployment guard.`}
	list := &cobra.Command{Use: "list <slug>", Short: "List the latest 100 retained drafts", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		data, err := draftRequest(cmd, cfg, "GET", "/api/apps/"+url.PathEscape(args[0])+"/drafts", nil, "", false)
		if err != nil {
			return err
		}
		format, err := resolveDeployFormat()
		if err != nil {
			return err
		}
		if format == formatJSON || format == formatNDJSON {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		}
		var res struct {
			Items []db.DeploymentDraft `json:"items"`
		}
		if err = json.Unmarshal(data, &res); err != nil {
			return err
		}
		for _, d := range res.Items {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  preview=%s  expires %s\n", d.ID, d.ContentDigest, d.PreviewSlug, time.Unix(d.ExpiresAt, 0).UTC().Format(time.RFC3339))
		}
		return nil
	}}
	var open bool
	preview := &cobra.Command{Use: "preview <slug> <draft-id>", Short: "Start or inspect a private preview", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		data, err := draftRequest(cmd, cfg, "POST", "/api/apps/"+url.PathEscape(args[0])+"/drafts/"+url.PathEscape(args[1])+"/preview", nil, "", false)
		if err != nil {
			return err
		}
		var d db.DeploymentDraft
		if err = json.Unmarshal(data, &d); err != nil {
			return err
		}
		if d.PreviewSlug == "" {
			return fmt.Errorf("server omitted preview slug")
		}
		if open {
			openAppURL(remoteAppURL(cfg.Host, d.PreviewSlug), false, cmd.ErrOrStderr())
		}
		format, err := resolveDeployFormat()
		if err != nil {
			return err
		}
		return printDraft(cmd, cfg, args[0], d, format)
	}}
	preview.Flags().BoolVar(&open, "open", false, "Open the private preview in your browser")
	var downtime, start bool
	promote := &cobra.Command{Use: "promote <slug> <draft-id>", Short: "Deploy the exact reviewed bundle to production", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		path := "/api/apps/" + url.PathEscape(args[0]) + "/drafts/" + url.PathEscape(args[1]) + "/promote"
		if start {
			path += "?start=true"
		}
		data, err := draftRequest(cmd, cfg, "POST", path, nil, "", downtime)
		if err != nil {
			return err
		}
		format, err := resolveDeployFormat()
		if err != nil {
			return err
		}
		if format == formatJSON || format == formatNDJSON {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		}
		var result struct {
			KeptStopped bool `json:"kept_stopped"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if note := formatKeptStoppedNote(result.KeptStopped, args[0]); note != "" {
			fmt.Fprintln(cmd.OutOrStdout(), note)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Promoted draft %s to %s\nURL: %s\n", args[1], args[0], remoteAppURL(cfg.Host, args[0]))
		return nil
	}}
	promote.Flags().BoolVar(&downtime, "allow-downtime", false, "Permit the normal stop-first deployment fallback")
	promote.Flags().BoolVar(&start, "start", false, "Start production if it was stopped")
	remove := &cobra.Command{Use: "delete <slug> <draft-id>", Short: "Delete a retained draft and its preview", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		data, err := draftRequest(cmd, cfg, "DELETE", "/api/apps/"+url.PathEscape(args[0])+"/drafts/"+url.PathEscape(args[1]), nil, "", false)
		if err != nil {
			return err
		}
		format, err := resolveDeployFormat()
		if err != nil {
			return err
		}
		if format == formatJSON || format == formatNDJSON {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Deleted draft %s and its preview\n", args[1])
		return nil
	}}
	cmd.AddCommand(list, preview, promote, remove)
	return cmd
}
