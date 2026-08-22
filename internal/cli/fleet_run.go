package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/rvben/shinyhub/internal/provenance"
)

// newRunID returns a random 32-hex-char id correlating every per-app call in
// one fleet run. New servers register this id before mutation and attach it to
// deployments and audit events; old servers safely ignore the header.
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// fleetUserAgent identifies fleet-originated requests in server logs/audit.
var fleetUserAgent = "shinyhub-fleet/" + version

const (
	fleetConvergenceInSync     = "in_sync"
	fleetConvergenceIncomplete = "incomplete"
)

// decorateFleetRequest stamps the run correlation id and a descriptive
// User-Agent on every fleet-originated request.
func decorateFleetRequest(req *http.Request, runID string) {
	req.Header.Set("X-Shinyhub-Run-Id", runID)
	req.Header.Set("User-Agent", fleetUserAgent)
}

func buildFleetProvenance(f *fleetApplyFlags, getenv func(string) string) (provenance.Metadata, error) {
	var m provenance.Metadata
	mode := strings.ToLower(strings.TrimSpace(f.provenanceMode))
	if mode != "auto" && mode != "none" {
		return m, fmt.Errorf("--provenance must be auto or none")
	}
	explicit := f.sourceProvider != "" || f.sourceURL != "" || f.sourceLabel != "" || f.jobURL != "" || f.jobLabel != "" || f.revision != "" || f.revisionRef != "" || f.revisionURL != "" || f.changeURL != "" || f.changeLabel != ""
	if mode == "none" {
		if explicit {
			return m, fmt.Errorf("explicit provenance flags cannot be used with --provenance=none")
		}
		return m, nil
	}
	if getenv("GITLAB_CI") != "" {
		m.Provider = "gitlab"
		pipelineLabel := "GitLab pipeline"
		if iid := getenv("CI_PIPELINE_IID"); iid != "" {
			pipelineLabel += " #" + iid
		}
		if u := getenv("CI_PIPELINE_URL"); u != "" {
			m.Source = &provenance.Link{Label: pipelineLabel, URL: u}
		}
		if u := getenv("CI_JOB_URL"); u != "" {
			label := getenv("CI_JOB_NAME")
			if label == "" {
				label = "GitLab job"
			}
			m.Job = &provenance.Link{Label: label, URL: u}
		}
		sha, ref := getenv("CI_COMMIT_SHA"), getenv("CI_COMMIT_REF_NAME")
		if sha != "" || ref != "" {
			revURL := ""
			if base := strings.TrimSuffix(getenv("CI_PROJECT_URL"), "/"); base != "" && sha != "" {
				revURL = base + "/-/commit/" + sha
			}
			m.Revision = &provenance.Revision{SHA: sha, Ref: ref, URL: revURL}
		}
		if iid := getenv("CI_MERGE_REQUEST_IID"); iid != "" {
			base := strings.TrimSuffix(getenv("CI_MERGE_REQUEST_PROJECT_URL"), "/")
			if base == "" {
				base = strings.TrimSuffix(getenv("CI_PROJECT_URL"), "/")
			}
			m.Change = &provenance.Link{Label: "MR !" + iid}
			if base != "" {
				m.Change.URL = base + "/-/merge_requests/" + iid
			}
		}
	}
	if f.sourceProvider != "" {
		m.Provider = f.sourceProvider
	}
	if f.sourceURL != "" || f.sourceLabel != "" {
		m.Source = &provenance.Link{Label: f.sourceLabel, URL: f.sourceURL}
		if m.Source.Label == "" {
			m.Source.Label = "Deployment source"
		}
	}
	if f.jobURL != "" || f.jobLabel != "" {
		m.Job = &provenance.Link{Label: f.jobLabel, URL: f.jobURL}
		if m.Job.Label == "" {
			m.Job.Label = "Deployment job"
		}
	}
	if f.revision != "" || f.revisionRef != "" || f.revisionURL != "" {
		m.Revision = &provenance.Revision{SHA: f.revision, Ref: f.revisionRef, URL: f.revisionURL}
	}
	if f.changeURL != "" || f.changeLabel != "" {
		m.Change = &provenance.Link{Label: f.changeLabel, URL: f.changeURL}
		if m.Change.Label == "" {
			m.Change.Label = "Change"
		}
	}
	if err := m.Validate(); err != nil {
		return provenance.Metadata{}, err
	}
	return m, nil
}

func registerFleetRun(cfg *cliConfig, runID, fleetID string, metadata provenance.Metadata) error {
	body, err := json.Marshal(map[string]any{"run_id": runID, "fleet_id": fleetID, "kind": "fleet_apply", "provenance": metadata})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.Host+"/api/fleet/runs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	req.Header.Set("Content-Type", "application/json")
	decorateFleetRequest(req, runID)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("register fleet run: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

// recordAppFleetState commits the declaration baseline only after an app has
// converged. On failure, status=incomplete records the attempt without replacing
// the prior successful baseline. Older servers never receive this request
// because fleet apply gates it on the advertised fleet_state capability.
func recordAppFleetState(cfg *cliConfig, slug, status, digest string, declared []fleet.ConfigDriftItem, message, runID string) error {
	values := make([]map[string]string, 0, len(declared))
	for _, item := range declared {
		values = append(values, map[string]string{"key": item.Key, "desired": item.Desired})
	}
	body, err := json.Marshal(map[string]any{
		"status": status, "desired_content_digest": digest,
		"declaration": values, "error": message,
	})
	if err != nil {
		return fmt.Errorf("encode fleet state for %s: %w", slug, err)
	}
	req, err := http.NewRequest(http.MethodPut, cfg.Host+"/api/apps/"+slug+"/fleet-state", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return sendFleetMutation(cfg, req, slug, nil, nil, runID)
}

// setPrecondition applies the If-Match-style headers the server enforces.
// ifDigest != nil sets X-Shinyhub-If-Content-Digest (server treats an empty
// value as "no assertion"). ifManagedBy != nil sets X-Shinyhub-If-Managed-By
// even when the pointed-to string is empty: header presence activates the
// server check and an empty value asserts the app is currently unmanaged.
func setPrecondition(req *http.Request, ifDigest *string, ifManagedBy *string) {
	if ifDigest != nil {
		req.Header.Set("X-Shinyhub-If-Content-Digest", *ifDigest)
	}
	if ifManagedBy != nil {
		req.Header.Set("X-Shinyhub-If-Managed-By", *ifManagedBy)
	}
}

// conflictError marks an app action aborted by a server precondition 409.
// apply records it, continues other apps, and never blind-retries it.
type conflictError struct {
	slug string
	msg  string
}

func (e *conflictError) Error() string {
	return fmt.Sprintf("conflict (state changed under us; re-run plan): %s", e.msg)
}

func isConflictError(err error) bool {
	var c *conflictError
	return errors.As(err, &c)
}

// isConflict reports whether an HTTP response is a precondition conflict.
func isConflict(resp *http.Response) bool { return resp.StatusCode == http.StatusConflict }
