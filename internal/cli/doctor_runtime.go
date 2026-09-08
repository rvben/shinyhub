package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rvben/shinyhub/internal/deploy"
)

func doctorRuntimeCapabilities(cfg *cliConfig, slug string, manifest *deploy.Manifest, hostRuntime ...*bool) doctorCheck {
	path := "/api/runtime-capabilities"
	if slug != "" {
		path = "/api/apps/" + url.PathEscape(slug) + "/capabilities"
	}
	query := ""
	if manifest != nil && manifest.App.Worker != nil && manifest.App.Worker.Isolation != nil {
		query = "?" + url.Values{"isolation": {*manifest.App.Worker.Isolation}}.Encode()
	}
	type feature struct {
		Supported bool   `json:"supported"`
		Reason    string `json:"reason"`
		Remedy    string `json:"remedy"`
	}
	var report struct {
		RequiresHostRuntime *bool              `json:"requires_host_runtime"`
		Isolation           string             `json:"isolation"`
		Features            map[string]feature `json:"features"`
	}
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest(http.MethodGet, cfg.Host+path+query, nil)
		if err != nil {
			return doctorFail("runtime-topology", "invalid capability URL", "Check the target host.", KindValidation, 1)
		}
		req.Header.Set("Authorization", authHeader(cfg.Token))
		resp, err := httpClient.Do(req)
		if err != nil {
			return doctorFail("runtime-topology", "runtime preflight could not reach the server", "Retry when the server is reachable.", KindValidation, 1)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && path != "/api/runtime-capabilities" {
			path = "/api/runtime-capabilities"
			continue
		}
		if resp.StatusCode != http.StatusOK || readErr != nil || json.Unmarshal(body, &report) != nil || len(report.Features) == 0 {
			return doctorFail("runtime-topology", fmt.Sprintf("runtime preflight returned an invalid response (HTTP %d)", resp.StatusCode), "Check server logs and CLI/server compatibility.", KindValidation, 1)
		}
		break
	}
	if report.RequiresHostRuntime != nil && len(hostRuntime) > 0 {
		*hostRuntime[0] = *report.RequiresHostRuntime
	}
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
	var reasons, remedies []string
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
	if len(reasons) > 0 {
		return doctorFail("runtime-topology", strings.Join(reasons, " "), strings.Join(remedies, " "), KindValidation, 1)
	}
	return doctorPass("runtime-topology", "selected isolation and declared producer/activation policies are supported by the target topology")
}
