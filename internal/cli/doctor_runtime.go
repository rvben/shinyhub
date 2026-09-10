package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rvben/shinyhub/internal/deploy"
)

// runtimeCapabilityFeature is the server's verdict on one topology feature:
// whether the selected isolation supports it and, when it does not, why and
// what to change.
type runtimeCapabilityFeature struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
	Remedy    string `json:"remedy"`
}

// runtimeCapabilityReport is the server's answer to the runtime-topology
// question: the isolation the app resolves to and the features that topology
// supports.
type runtimeCapabilityReport struct {
	RequiresHostRuntime *bool                               `json:"requires_host_runtime"`
	Isolation           string                              `json:"isolation"`
	Features            map[string]runtimeCapabilityFeature `json:"features"`
}

// fetchRuntimeCapabilities asks GET /api/apps/{slug}/capabilities, with the
// manifest's isolation projected, and falls back to /api/runtime-capabilities
// for a slug the server does not know. Failures are typed so a caller can tell
// a server it could not reach (*url.Error), a refusal (*httpStatusError) and
// an undecodable reply (*protocolError) apart from a policy verdict.
func fetchRuntimeCapabilities(cfg *cliConfig, slug string, manifest *deploy.Manifest) (runtimeCapabilityReport, error) {
	var report runtimeCapabilityReport
	path := "/api/runtime-capabilities"
	if slug != "" {
		path = "/api/apps/" + url.PathEscape(slug) + "/capabilities"
	}
	query := ""
	if manifest != nil && manifest.App.Worker != nil && manifest.App.Worker.Isolation != nil {
		query = "?" + url.Values{"isolation": {*manifest.App.Worker.Isolation}}.Encode()
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(http.MethodGet, cfg.Host+path+query, nil)
		if err != nil {
			return report, fmt.Errorf("invalid capability URL: %w", err)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return report, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && path != "/api/runtime-capabilities" {
			path = "/api/runtime-capabilities"
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return report, httpError(cfg.Token, "runtime preflight", resp, body)
		}
		if readErr != nil {
			return report, &protocolError{op: "runtime preflight", err: readErr}
		}
		if err := json.Unmarshal(body, &report); err != nil {
			return report, &protocolError{op: "runtime preflight", err: err}
		}
		if len(report.Features) == 0 {
			return report, &protocolError{op: "runtime preflight", err: errors.New("response reports no features")}
		}
		return report, nil
	}
	return report, &protocolError{op: "runtime preflight", err: errors.New("no capability endpoint answered")}
}

// runtimeTopologyProblems evaluates a report against the isolation the
// manifest selects and the schedule behaviours it declares (deploy-triggered
// producers, roll-on-success activation). reasons and remedies are parallel:
// one entry per unsupported or unreported feature.
func runtimeTopologyProblems(report runtimeCapabilityReport, manifest *deploy.Manifest) (reasons, remedies []string) {
	required := []string{report.Isolation}
	if manifest != nil {
		for _, schedule := range manifest.Schedules {
			if schedule.DeployTrigger != "" && schedule.DeployTrigger != "never" {
				required = append(required, "deploy_producers")
			}
			if schedule.OnSuccess == "roll" {
				required = append(required, "data_activation")
			}
		}
	}
	seen := map[string]bool{}
	for _, name := range required {
		if seen[name] {
			continue
		}
		seen[name] = true
		f, ok := report.Features[name]
		if !ok {
			reasons = append(reasons, "server did not report "+name)
			remedies = append(remedies, "Upgrade the server to report the selected runtime capability.")
			continue
		}
		if !f.Supported {
			reasons = append(reasons, name+": "+f.Reason)
			remedies = append(remedies, f.Remedy)
		}
	}
	return reasons, remedies
}

// doctorRuntimeCapabilities is the doctor check over fetchRuntimeCapabilities
// and runtimeTopologyProblems. Every failure, including one the server could
// not answer, is a failed check here: doctor reports, it does not retry.
func doctorRuntimeCapabilities(cfg *cliConfig, slug string, manifest *deploy.Manifest, hostRuntime ...*bool) doctorCheck {
	report, err := fetchRuntimeCapabilities(cfg, slug, manifest)
	if err != nil {
		var ue *url.Error
		var hse *httpStatusError
		var pe *protocolError
		switch {
		case errors.As(err, &ue):
			return doctorFail("runtime-topology", "runtime preflight could not reach the server", "Retry when the server is reachable.", KindValidation, 1)
		case errors.As(err, &hse):
			return doctorFail("runtime-topology", fmt.Sprintf("runtime preflight returned an invalid response (HTTP %d)", hse.Status), "Check server logs and CLI/server compatibility.", KindValidation, 1)
		case errors.As(err, &pe):
			return doctorFail("runtime-topology", fmt.Sprintf("runtime preflight returned an invalid response (HTTP %d)", http.StatusOK), "Check server logs and CLI/server compatibility.", KindValidation, 1)
		default:
			return doctorFail("runtime-topology", "invalid capability URL", "Check the target host.", KindValidation, 1)
		}
	}
	if report.RequiresHostRuntime != nil && len(hostRuntime) > 0 {
		*hostRuntime[0] = *report.RequiresHostRuntime
	}
	reasons, remedies := runtimeTopologyProblems(report, manifest)
	if len(reasons) > 0 {
		return doctorFail("runtime-topology", strings.Join(reasons, " "), strings.Join(remedies, " "), KindValidation, 1)
	}
	return doctorPass("runtime-topology", "selected isolation and declared producer/activation policies are supported by the target topology")
}
