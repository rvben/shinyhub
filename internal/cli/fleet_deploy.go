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
func deployAppBundle(cfg *cliConfig, slug, dir, visibility string, out io.Writer, runID string) (string, error) {
	if err := ensureFleetApp(cfg, slug, visibility, out); err != nil {
		return "", err
	}
	buf, summary, err := zipDir(dir)
	if err != nil {
		return "", fmt.Errorf("bundle %s: %w", slug, err)
	}
	if summary != "" {
		fmt.Fprintf(out, "  %s: %s\n", slug, summary)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, buf); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", cfg.Host+"/api/apps/"+slug+"/deploy", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	decorateFleetRequest(req, runID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("deploy %s: %w", slug, err)
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("deploy %s failed: HTTP %d: %s", slug, resp.StatusCode, string(rb))
	}

	if err := waitForFleetHealthy(cfg, slug, out); err != nil {
		return "", err
	}
	return readPromotedDigest(cfg, slug)
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
