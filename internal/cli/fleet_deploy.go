package cli

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// fleetHealthTimeout bounds the post-deploy health wait. First-run uv syncs
// can take minutes, so this is generous relative to the 60s interactive
// `deploy --wait` default.
const fleetHealthTimeout = 120 * time.Second

// deployAppBundle deploys one app's local directory through the existing
// per-app deploy mechanism (ensure app exists with the requested visibility,
// bundle, upload, wait for health), then re-reads the app from the server
// and returns its freshly promoted content_digest. Returning the post-deploy
// digest lets a same-run config PATCH carry a precondition built from the
// deployment this run just performed (otherwise it would 409 against us).
//
// committed reports whether the server accepted the bundle: it is true only
// when POST /api/apps/{slug}/deploy returned 2xx, in which case the bundle is
// live even if a later step (health wait / digest readback) then fails. A
// non-2xx response is reported committed=false because the deploy endpoint
// returns 500 both BEFORE promotion (BeginDeployment, quota, deploy.Run then
// restore) and AFTER it (PromoteDeployment record failure, manifest schedule
// apply), so the status alone cannot tell whether the new bundle went live -
// callers that care (adopt) resolve that authoritatively with a digest
// readback.
func deployAppBundle(cfg *cliConfig, slug, dir, visibility string, out io.Writer, runID string) (promoted string, committed bool, err error) {
	if err := ensureFleetApp(cfg, slug, visibility, out); err != nil {
		return "", false, err
	}
	buf, summary, err := zipDir(dir)
	if err != nil {
		return "", false, fmt.Errorf("bundle %s: %w", slug, err)
	}
	if summary != "" {
		fmt.Fprintf(out, "  %s: %s\n", slug, summary)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		return "", false, err
	}
	if _, err := io.Copy(part, buf); err != nil {
		return "", false, err
	}
	if err := writer.Close(); err != nil {
		return "", false, err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/deploy", &body)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	decorateFleetRequest(req, runID)
	// Deploy can take several minutes on first run (uv downloads packages).
	// Use http.DefaultClient (no timeout) to match the SSE logs command.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("deploy %s: %w", slug, err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("deploy %s failed: HTTP %d: %s", slug, resp.StatusCode, string(rb))
	}

	// Bundle accepted: from here on the deploy is committed even if a
	// post-deploy step fails.
	if err := waitForFleetHealthy(cfg, slug, out); err != nil {
		return "", true, err
	}
	promoted, err = readPromotedDigest(cfg, slug)
	return promoted, true, err
}

// readPromotedDigest re-GETs the app list and returns the live (succeeded)
// content_digest for slug, or "" if the server does not expose one.
func readPromotedDigest(cfg *cliConfig, slug string) (string, error) {
	apps, err := fetchApps(cfg)
	if err != nil {
		return "", fmt.Errorf("read back digest for %s: %w", slug, err)
	}
	for _, a := range apps {
		if a.Slug == slug {
			return a.ContentDigest, nil
		}
	}
	return "", nil
}

// ensureFleetApp ensures the app exists with the requested visibility,
// delegating to the existing per-app create/verify helper. That helper issues
// GET /api/apps/{slug} then POST /api/apps with {"slug","name","access"} when
// absent; visibility is forwarded as the access value.
func ensureFleetApp(cfg *cliConfig, slug, visibility string, out io.Writer) error {
	return ensureAppWithOutput(cfg, slug, visibility, out)
}

// waitForFleetHealthy blocks until the app reports running or a terminal
// failure, delegating to the existing health-wait helper with a fleet-sized
// timeout.
func waitForFleetHealthy(cfg *cliConfig, slug string, out io.Writer) error {
	return waitForHealthyWithOutput(cfg, slug, fleetHealthTimeout, out)
}
