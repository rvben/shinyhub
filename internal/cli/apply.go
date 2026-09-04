package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/spf13/cobra"
)

func newApplyCmd() *cobra.Command {
	var allowDowntime bool
	cmd := &cobra.Command{
		Use:   "apply PLAN",
		Short: "Apply an exact saved plan",
		Long: `Verify and apply a plan produced by 'shinyhub plan --out'.

Apply validates the artifact's format, integrity, embedded bundle digest,
expiry, and target before making a request. It uploads the exact bundle stored
in the plan and asks the server to atomically compare the resource revision
observed during planning before changing the app. It never reads or rebuilds
the original working directory. By default a working app is preserved when the
server cannot perform a safe handoff; --allow-downtime explicitly permits the
stop-first fallback and disconnects active sessions.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(cmd, args[0], allowDowntime)
		},
	}
	cmd.Flags().BoolVar(&allowDowntime, "allow-downtime", false, "Permit an explicit stop-first deployment when safe version handoff is unavailable (disconnects active sessions)")
	return cmd
}

func runApply(cmd *cobra.Command, path string, allowDowntime bool) error {
	// The complete local trust boundary comes first. A corrupt, modified, or
	// expired plan cannot trigger config loading or any network request.
	loaded, err := readSavedPlan(path, time.Now(), false)
	if err != nil {
		return &ExitCodeError{Code: 1, Kind: KindValidation, Err: err}
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	plannedHost := normalizeHost(loaded.Envelope.Target.Host)
	currentHost := normalizeHost(cfg.Host)
	if currentHost != plannedHost {
		return &ExitCodeError{Code: 1, Kind: KindValidation, Err: fmt.Errorf(
			"saved plan targets %s, but the selected host is %s; use `--host %s` or re-run plan",
			plannedHost, currentHost, plannedHost)}
	}
	info, err := probeServer(cfg)
	if err != nil {
		return err
	}
	if !info.Capabilities.PlanApply {
		return fmt.Errorf("the selected server does not support atomic saved-plan apply; upgrade the server and re-run `shinyhub plan --out`")
	}

	format, err := resolveDeployFormat()
	if err != nil {
		return err
	}
	slug := loaded.Envelope.Target.Slug
	revision := loaded.Envelope.Target.ExpectedRevision
	if !loaded.Envelope.Target.ExpectedExists {
		revision, err = createAppForSavedPlan(cfg, loaded.Envelope)
		if err != nil {
			return err
		}
	}

	if !quietFlag && format != formatNDJSON {
		fmt.Fprintf(cmd.ErrOrStderr(), "Applying %s to %s using exact bundle %s...\n",
			loaded.Envelope.PlanID, slug, shortDigest(loaded.Envelope.Bundle.Digest))
	}
	resp, err := postSavedPlanBundle(cfg, loaded, revision, allowDowntime)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out []byte
	if isDeployEventResponse(resp) {
		out, err = consumeDeployEvents(resp, format, cmd.OutOrStdout(), cmd.ErrOrStderr(), quietFlag)
		if err != nil {
			var status *httpStatusError
			if errors.As(err, &status) && status.Status == http.StatusConflict &&
				resp.Header.Get(conflictHeader) != conflictHandoffDeferred {
				return staleSavedPlanError(path, slug, err)
			}
			return err
		}
	} else {
		out, _ = io.ReadAll(resp.Body)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		httpErr := httpError(cfg.Token, "apply saved plan", resp, out)
		if resp.StatusCode == http.StatusConflict &&
			resp.Header.Get(conflictHeader) != conflictHandoffDeferred {
			return staleSavedPlanError(path, slug, httpErr)
		}
		return httpErr
	}
	var app map[string]any
	_ = json.Unmarshal(out, &app)
	if format == formatJSON {
		result := map[string]any{
			"status": "applied", "plan_id": loaded.Envelope.PlanID,
			"slug": slug, "bundle_digest": loaded.Envelope.Bundle.Digest,
			"url": remoteAppURL(cfg.Host, slug),
		}
		if v, ok := app["deploy_count"]; ok {
			result["deploy_count"] = v
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
	}
	if format != formatNDJSON {
		fmt.Fprintf(cmd.OutOrStdout(), "✓ Applied %s exactly\n  App: %s\n  Bundle: %s\n  URL: %s\n",
			loaded.Envelope.PlanID, slug, loaded.Envelope.Bundle.Digest, remoteAppURL(cfg.Host, slug))
	}
	return nil
}

func staleSavedPlanError(path, slug string, cause error) error {
	return &ExitCodeError{Code: 2, Kind: KindConflict, Err: &hintedMsgError{
		msg: cause.Error(), hint: fmt.Sprintf("remote state changed; re-run `shinyhub plan . --slug %s --out %s --force`", slug, shellQuote(path)), cause: cause,
	}}
}

func createAppForSavedPlan(cfg *cliConfig, envelope savedPlanEnvelope) (string, error) {
	body := map[string]string{"slug": envelope.Target.Slug, "name": envelope.Desired.Name}
	if visibility := envelope.Desired.Visibility; visibility != "" && visibility != "server default" {
		body["access"] = visibility
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.Host+"/api/apps", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		httpErr := httpError(cfg.Token, "apply create app", resp, responseBody)
		if resp.StatusCode == http.StatusConflict {
			return "", &ExitCodeError{Code: 2, Kind: KindConflict, Err: &hintedMsgError{
				msg:  "saved plan expected the app to be absent, but its slug is now in use",
				hint: "remote state changed; re-run `shinyhub plan`", cause: httpErr,
			}}
		}
		return "", httpErr
	}
	revision := resp.Header.Get("X-Shinyhub-Resource-Revision")
	if revision == "" {
		return "", errors.New("server created the app but did not return a resource revision; upgrade the server before retrying")
	}
	return revision, nil
}

func postSavedPlanBundle(cfg *cliConfig, loaded *loadedSavedPlan, revision string, allowDowntime bool) (*http.Response, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", savedPlanBundlePath)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(loaded.Bundle); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	endpoint := cfg.Host + "/api/apps/" + url.PathEscape(loaded.Envelope.Target.Slug) + "/deploy"
	if loaded.Envelope.Desired.Start {
		endpoint += "?start=true"
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", deployevent.MediaType)
	req.Header.Set("X-Shinyhub-Deploy-Channel", "cli")
	req.Header.Set("X-Shinyhub-If-Resource-Revision", revision)
	if allowDowntime {
		req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	}
	return streamClient.Do(req)
}
