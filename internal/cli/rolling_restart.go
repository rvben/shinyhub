package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

func waitForRollingActivation(ctx context.Context, cfg *cliConfig, slug string, id int64, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/apps/%s/activations/%d", cfg.Host, url.PathEscape(slug), id), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("wait for rolling restart %d: %w", id, err)
		}
		out, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if resp.StatusCode >= 400 {
			return httpError(cfg.Token, "rolling restart status", resp, out)
		}
		var state struct {
			Status      string `json:"status"`
			LastError   string `json:"last_error"`
			DeferReason string `json:"defer_reason"`
		}
		if err := json.Unmarshal(out, &state); err != nil {
			return err
		}
		switch state.Status {
		case "succeeded":
			return nil
		case "failed", "superseded", "not_needed", "blocked_unsupported", "target_deleted", "cancelled":
			return fmt.Errorf("rolling restart %d ended as %s: %s", id, state.Status, state.LastError)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("rolling restart %d still %s (%s): %w", id, state.Status, state.DeferReason, ctx.Err())
		case <-timer.C:
		}
	}
}
