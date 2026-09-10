package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/fleet"
)

// fleetServerPreflightProblem is one rejection the server would raise for an
// app the plan is about to create, adopt, or redeploy. Stage "deploy" carries
// the exact text POST /api/apps/{slug}/deploy would have returned; stage
// "settings" is the post-deploy fleet config PATCH judged by server policy.
type fleetServerPreflightProblem struct {
	Slug    string
	Stage   string
	Message string
}

func (p fleetServerPreflightProblem) String() string {
	if p.Stage == "settings" {
		return fmt.Sprintf("app %q [app.config]: %s", p.Slug, p.Message)
	}
	return fmt.Sprintf("app %q: %s", p.Slug, p.Message)
}

// deployPreflightRequest mirrors the server's POST /api/apps/{slug}/deploy-preflight
// body: the bundle manifest exactly as it will be uploaded, the detected app
// type, and the fleet [app.config] keys the server holds policy for.
type deployPreflightRequest struct {
	Manifest string                   `json:"manifest,omitempty"`
	AppType  string                   `json:"app_type,omitempty"`
	Settings *deployPreflightSettings `json:"settings,omitempty"`
}

type deployPreflightSettings struct {
	Replicas  *int           `json:"replicas,omitempty"`
	Autoscale map[string]any `json:"autoscale,omitempty"`
}

type deployPreflightResponse struct {
	Valid     bool   `json:"valid"`
	Isolation string `json:"isolation"`
	Problems  []struct {
		Stage   string `json:"stage"`
		Message string `json:"message"`
	} `json:"problems"`
}

// fleetBundleFacts is what the rehearsal knows about one resolved bundle: the
// directory holding its shinyhub.toml, the parsed manifest (nil when the
// bundle has none), and the app type the server will detect from the upload.
// AppType comes from the upload's own file list rather than the source
// directory, so an entrypoint contributed by a shared [[bundle_file]] input or
// kept out by the ignore file is judged as the extracted bundle will be.
type fleetBundleFacts struct {
	Dir      string
	Manifest *deploy.Manifest
	AppType  string
}

// resolveFleetBundleFacts derives the facts for one bundle from the source
// directory and the preview of the upload built from it.
func resolveFleetBundleFacts(spec bundleBuildSpec, manifest *deploy.Manifest, preview *bundlePreview) fleetBundleFacts {
	uploaded := make(map[string]bool, len(preview.Files))
	for _, name := range preview.Files {
		uploaded[name] = true
	}
	return fleetBundleFacts{
		Dir:      spec.Dir,
		Manifest: manifest,
		AppType:  deploy.DetectAppTypeIn(spec.Dir, func(name string) bool { return uploaded[name] }),
	}
}

// preflightsDeploy reports whether a diff action ends in a bundle deploy. A
// config-only update is a PATCH the server validates in place and rejects
// before changing anything, so it needs no rehearsal; a delete uploads nothing.
func preflightsDeploy(action fleet.Action) bool {
	switch action {
	case fleet.ActionCreate, fleet.ActionAdopt, fleet.ActionUpdateSource, fleet.ActionUpdateSourceConfig:
		return true
	}
	return false
}

// fleetServerPreflight rehearses every deploy the diff implies against the
// server before anything is changed. Each app that would be created, adopted,
// or redeployed is checked with the validators the deploy handler itself runs,
// so a manifest the server would reject with 400 (an elastic pool combined
// with a data-producing schedule, a replica count above the server ceiling, an
// R bundle on a Fargate tier) surfaces at plan time with the deploy's own
// message, instead of after the first apps in the manifest have already been
// converged.
//
// The endpoint arrived with the deploy_preflight capability. Against an older
// server that only reports runtime capabilities, the check degrades to the
// runtime-topology probe doctor uses, which covers the producer and roll
// guards but not the deploy's other policy checks. A server with neither is
// left alone, exactly as before.
//
// Returns the problems found. A question the server did not answer (transport
// failure, refusal, undecodable reply) is returned as a typed error, never as
// a problem, so the caller reports it with its own kind and exit code rather
// than as a rejection the operator would go and fix in the manifest.
func fleetServerPreflight(cfg *cliConfig, caps serverCaps, m *fleet.Manifest, diff []fleet.AppDiff,
	bundles map[string]fleetBundleFacts) ([]fleetServerPreflightProblem, error) {
	if !caps.DeployPreflight && !caps.RuntimeCapabilities {
		return nil, nil
	}
	entries := make(map[string]*fleet.AppEntry, len(m.Apps))
	for i := range m.Apps {
		entries[m.Apps[i].Slug] = &m.Apps[i]
	}
	var problems []fleetServerPreflightProblem
	for _, d := range diff {
		if !preflightsDeploy(d.Action) {
			continue
		}
		entry, ok := entries[d.Slug]
		if !ok {
			continue
		}
		facts, ok := bundles[d.Slug]
		if !ok {
			continue
		}
		if !caps.DeployPreflight {
			fallback, err := runtimeTopologyFallback(cfg, d.Slug, facts.Manifest)
			if err != nil {
				return nil, err
			}
			problems = append(problems, fallback...)
			continue
		}
		req, err := buildDeployPreflightRequest(facts, entry)
		if err != nil {
			return nil, err
		}
		reply, err := postDeployPreflight(cfg, d.Slug, req)
		if err != nil {
			return nil, err
		}
		for _, p := range reply.Problems {
			problems = append(problems, fleetServerPreflightProblem{Slug: d.Slug, Stage: p.Stage, Message: p.Message})
		}
	}
	return problems, nil
}

// buildDeployPreflightRequest reads the bundle manifest from the resolved
// source directory. Shared [[bundle_file]] inputs can never target
// shinyhub.toml (it is a reserved control file), so the file on disk is the
// manifest the deploy will upload.
func buildDeployPreflightRequest(facts fleetBundleFacts, entry *fleet.AppEntry) (deployPreflightRequest, error) {
	req := deployPreflightRequest{AppType: facts.AppType}
	data, err := os.ReadFile(filepath.Join(facts.Dir, deploy.ManifestFilename))
	switch {
	case err == nil:
		req.Manifest = string(data)
	case errors.Is(err, os.ErrNotExist):
	default:
		return req, fmt.Errorf("app %q: read %s: %w", entry.Slug, deploy.ManifestFilename, err)
	}
	if entry.Config.Replicas != nil || entry.Config.Autoscale != nil {
		req.Settings = &deployPreflightSettings{Replicas: entry.Config.Replicas}
		if entry.Config.Autoscale != nil {
			req.Settings.Autoscale = autoscalePatchBody(entry.Config.Autoscale)
		}
	}
	return req, nil
}

// postDeployPreflight issues one POST /api/apps/{slug}/deploy-preflight. A
// non-200 reply is not a verdict on the deploy, so it is returned as the
// typed HTTP error every other request uses: a 5xx classifies as a server
// failure, a 401/403 as a credential problem, never as a validation result.
func postDeployPreflight(cfg *cliConfig, slug string, body deployPreflightRequest) (deployPreflightResponse, error) {
	var reply deployPreflightResponse
	b, err := json.Marshal(body)
	if err != nil {
		return reply, fmt.Errorf("encode preflight body for %s: %w", slug, err)
	}
	req, err := http.NewRequest(http.MethodPost, cfg.Host+"/api/apps/"+url.PathEscape(slug)+"/deploy-preflight", bytes.NewReader(b))
	if err != nil {
		return reply, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return reply, fmt.Errorf("deploy preflight for %s: %w", slug, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return reply, fmt.Errorf("deploy preflight for %s: read response: %w", slug, err)
	}
	if resp.StatusCode != http.StatusOK {
		return reply, httpError(cfg.Token, "deploy preflight for "+slug, resp, raw)
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return reply, &protocolError{op: "deploy preflight for " + slug, err: err}
	}
	return reply, nil
}

// runtimeTopologyFallback is the pre-deploy_preflight degradation: the
// runtime-capabilities probe answers only whether the isolation the bundle
// selects supports producer and roll schedules, so it is consulted only when
// the bundle declares one. A verdict pairs the server's reason with its
// remedy, the same line doctor prints; a probe the server did not answer is
// returned as the typed transport error, not as a verdict.
func runtimeTopologyFallback(cfg *cliConfig, slug string, bm *deploy.Manifest) ([]fleetServerPreflightProblem, error) {
	if bm == nil || !manifestDeclaresActivation(bm) {
		return nil, nil
	}
	report, err := fetchRuntimeCapabilities(cfg, slug, bm)
	if err != nil {
		return nil, fmt.Errorf("runtime preflight for %s: %w", slug, err)
	}
	reasons, remedies := runtimeTopologyProblems(report, bm)
	if len(reasons) == 0 {
		return nil, nil
	}
	msg := strings.Join(reasons, " ")
	if fix := strings.Join(remedies, " "); fix != "" {
		msg += " " + fix
	}
	return []fleetServerPreflightProblem{{Slug: slug, Stage: "deploy", Message: msg}}, nil
}

// manifestDeclaresActivation reports whether any schedule in the bundle
// manifest publishes data (deploy_trigger other than never) or rolls the pool
// on success, the two behaviours the runtime-capabilities probe fences.
func manifestDeclaresActivation(bm *deploy.Manifest) bool {
	for _, schedule := range bm.Schedules {
		if schedule.DeployTrigger != "" && schedule.DeployTrigger != "never" {
			return true
		}
		if schedule.OnSuccess == "roll" {
			return true
		}
	}
	return false
}
