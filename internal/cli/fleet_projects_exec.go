package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/fleet"
)

// applyOutcome bundles the two convergence passes so the report, the JSON
// envelope and the exit code all see the same set. Projects are kept separate
// from apps for display (they are not deployed and have no digest) but are
// tallied together, so a project that could not be named makes the run
// PARTIAL rather than silently reporting OK.
type applyOutcome struct {
	projects []applyResult
	apps     []applyResult
}

func (o applyOutcome) all() []applyResult {
	out := make([]applyResult, 0, len(o.projects)+len(o.apps))
	out = append(out, o.projects...)
	return append(out, o.apps...)
}

// convergeProjects reconciles the manifest's [[project]] blocks. It runs to
// completion BEFORE the app loop so an app referencing a declared project finds
// it already named within a single apply; with the app loop first, the deploy
// lazily creates an unnamed projects row and the dashboard shows the raw slug
// until the project pass catches up.
//
// It deploys nothing, so there is no retry, no precondition and no
// concurrency: each action is a single small request. Continue-on-error,
// matching convergeFleet.
func convergeProjects(cfg *cliConfig, pf *preflightResult, opt convergeOpts, out io.Writer) []applyResult {
	entries := make(map[string]fleet.ProjectEntry, len(pf.manifest.Projects))
	for _, p := range pf.manifest.Projects {
		entries[p.Slug] = p
	}

	results := make([]applyResult, 0, len(pf.projectDiff))
	for _, d := range pf.projectDiff {
		start := time.Now()
		r := applyResult{slug: d.Slug, action: d.Action}
		entry := entries[d.Slug]

		switch d.Action {
		case fleet.ActionUnchanged:
			r.status, r.duration = statusUnchanged, time.Since(start)
		case fleet.ActionCreate:
			if err := createProject(cfg, entry, opt.runID); err != nil {
				r.status, r.err = statusFailed, err
			} else {
				r.status = statusCreated
			}
			r.duration = time.Since(start)
		case fleet.ActionUpdateConfig:
			if err := patchProject(cfg, entry, driftedKeys(d.Drift), opt.runID); err != nil {
				r.status, r.err = statusFailed, err
			} else {
				r.status = statusUpdated
			}
			r.duration = time.Since(start)
		default:
			r.status, r.note = statusSkipped, "unrecognized project action"
			r.duration = time.Since(start)
		}
		if r.err != nil {
			fmt.Fprintf(out, "  project %s: %v\n", d.Slug, r.err)
		}
		results = append(results, r)
	}
	return results
}

// driftedKeys reduces drift items to their keys. The drift item's Desired is a
// quoted display string, not a value to send, so the body is rebuilt from the
// declared entry - the same rule applyConfigDrift follows for name and
// description.
func driftedKeys(drift []fleet.ConfigDriftItem) map[string]bool {
	keys := make(map[string]bool, len(drift))
	for _, it := range drift {
		keys[it.Key] = true
	}
	return keys
}

// projectBody maps the manifest's declared keys onto the API's field names.
// The manifest key is `icon`; the API field is `icon_emoji`.
func projectBody(p fleet.ProjectEntry, only map[string]bool) map[string]any {
	body := map[string]any{}
	if p.Name != nil && (only == nil || only["name"]) {
		body["name"] = *p.Name
	}
	if p.Description != nil && (only == nil || only["description"]) {
		body["description"] = *p.Description
	}
	if p.Icon != nil && (only == nil || only["icon"]) {
		body["icon_emoji"] = *p.Icon
	}
	return body
}

// doFleetJSON issues a fleet-decorated JSON request and returns the response
// with its body already drained and closed, so callers can branch on the status
// code and still hand the bytes to httpError.
//
// It deliberately does not go through sendFleetMutation (fleet_patch.go:18),
// which the app path uses: that helper collapses every 2xx into a bare nil, and
// createProject must tell 201 (created with metadata) from 200 (already existed,
// so the metadata still needs a PATCH). Projects also carry no content-digest or
// managed-by preconditions, which is the rest of what sendFleetMutation adds.
func doFleetJSON(cfg *cliConfig, method, path string, body map[string]any, runID string) (*http.Response, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("encode %s body: %w", path, err)
	}
	req, err := http.NewRequest(method, cfg.Host+path, bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(cfg.Token))
	decorateFleetRequest(req, runID)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw, nil
}

// createProject POSTs the project WITH its declared metadata, so it is created
// already named. A 200 (rather than 201) means the slug was created between the
// plan and the apply, e.g. by a concurrent deploy of another app in the same
// project; the idempotent POST deliberately does not overwrite an existing row,
// so the metadata is applied with a follow-up PATCH. This is the only place the
// two calls are chained.
func createProject(cfg *cliConfig, p fleet.ProjectEntry, runID string) error {
	body := projectBody(p, nil)
	body["slug"] = p.Slug
	resp, raw, err := doFleetJSON(cfg, http.MethodPost, "/api/projects", body, runID)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusOK {
		return patchProject(cfg, p, nil, runID)
	}
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "create project "+p.Slug, resp, raw)
	}
	return nil
}

// patchProject PATCHes the given keys, or every declared key when only is nil.
func patchProject(cfg *cliConfig, p fleet.ProjectEntry, only map[string]bool, runID string) error {
	body := projectBody(p, only)
	if len(body) == 0 {
		return nil
	}
	resp, raw, err := doFleetJSON(cfg, http.MethodPatch, "/api/projects/"+p.Slug, body, runID)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return httpError(cfg.Token, "update project "+p.Slug, resp, raw)
	}
	return nil
}
