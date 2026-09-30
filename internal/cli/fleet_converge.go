package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fleet"
)

// fetchFleetApp reads GET /api/apps/{slug} and returns the app record from its
// {"app": ...} envelope. It is the per-app read apply uses between mutations.
func fetchFleetApp(cfg *cliConfig, slug string) (*db.App, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.Host+"/api/apps/"+slug, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", slug, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, httpError(cfg.Token, "read app "+slug, resp, body)
	}
	var env struct {
		App *db.App `json:"app"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, &protocolError{op: "decode app " + slug, err: err}
	}
	if env.App == nil {
		return nil, &protocolError{op: "decode app " + slug, err: fmt.Errorf("response has no app object")}
	}
	return env.App, nil
}

// convergeDeclaredConfig makes the server's stored settings match the
// effective declaration after a deploy. The deploy applied the new bundle's
// own [app] values, which can replace a fleet-declared value that matched when
// the plan was computed, so the pre-deploy drift list no longer describes what
// needs sending. It re-reads the app, PATCHes exactly the keys that differ now
// (a key the deploy left correct is not re-sent, which matters for keys like
// replicas that the server rejects or redeploys on), and re-reads once more to
// confirm nothing is left. Remaining drift is an error, never a success.
func convergeDeclaredConfig(cfg *cliConfig, slug string, entry fleet.AppEntry, ifD, ifMB *string, opt convergeOpts) (int, error) {
	app, err := fetchFleetApp(cfg, slug)
	if err != nil {
		return 0, fmt.Errorf("read settings after deploy: %w", err)
	}
	drift := fleet.ConfigDrift(entry, observedFromApp(*app))
	if len(drift) == 0 {
		return 0, nil
	}
	attempts, err := applyConfigDriftWithRetry(cfg, slug, drift, fleet.EffectiveConfig(entry), ifD, ifMB, opt.runID, opt.retries)
	if err != nil {
		return attempts, err
	}
	app, err = fetchFleetApp(cfg, slug)
	if err != nil {
		return attempts, fmt.Errorf("read settings after config patch: %w", err)
	}
	if left := fleet.ConfigDrift(entry, observedFromApp(*app)); len(left) > 0 {
		return attempts, fmt.Errorf("declared config did not converge: %s", describeDrift(left))
	}
	return attempts, nil
}

// describeDrift renders drift items as "key server -> desired" for errors.
func describeDrift(items []fleet.ConfigDriftItem) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s %s -> %s", it.Key, it.Server, it.Desired))
	}
	return strings.Join(parts, ", ")
}
