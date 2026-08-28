package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"

	"github.com/spf13/cobra"
)

// fleet verify is an intentionally read-only incident entry point. It reads
// aggregate serving health and per-schedule producer provenance without a
// manifest and never calls a reconcile, run, deploy, or mutation endpoint.
type fleetVerifyHealth struct {
	Complete bool `json:"complete"`
	Apps     struct {
		Total, Running, Idle, Stopped, Degraded, Crashed int
	} `json:"apps"`
	Replicas struct {
		Running, Lost, Stopped int
	} `json:"replicas"`
}

type fleetVerifySchedule struct {
	Slug                   string `json:"slug"`
	Schedule               string `json:"schedule"`
	Enabled                bool   `json:"enabled"`
	Stale                  *bool  `json:"stale"`
	FreshnessError         string `json:"freshness_error"`
	DeployTrigger          string `json:"deploy_trigger"`
	DeployTriggerSatisfied bool   `json:"deploy_trigger_satisfied"`
	ProducerRepairRequired bool   `json:"producer_repair_required"`
	CurrentAppVersion      string `json:"current_app_version"`
	CurrentContentDigest   string `json:"current_content_digest"`
	ProducerAppVersion     string `json:"producer_app_version"`
	ProducerContentDigest  string `json:"producer_content_digest"`
	ConvergenceStatus      string `json:"convergence_status"`
	ConvergenceError       string `json:"convergence_error"`
}

type fleetVerifyIssue struct {
	Kind     string `json:"kind"`
	Resource string `json:"resource"`
	Detail   string `json:"detail"`
}

type fleetVerifyResult struct {
	OK        bool                  `json:"ok"`
	Health    fleetVerifyHealth     `json:"health"`
	Schedules []fleetVerifySchedule `json:"schedules"`
	Issues    []fleetVerifyIssue    `json:"issues"`
}

func newFleetVerifyCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Read-only serving-health and code/data compatibility audit",
		Long: "Reads fleet health and every schedule's freshness plus exact producer bundle provenance.\n" +
			"Makes two GET requests and never deploys, reconciles, triggers a run, or changes server state.",
		RunE: func(cmd *cobra.Command, _ []string) error { return runFleetVerify(cmd, jsonOutput) },
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit one machine-readable result")
	return cmd
}

func fleetVerifyGET(cfg *cliConfig, path string, target any) error {
	req, err := http.NewRequest(http.MethodGet, cfg.Host+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeader(cfg.Token))
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return httpError(cfg.Token, "verify fleet", resp, body)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func runFleetVerify(cmd *cobra.Command, jsonOutput bool) error {
	if format, err := resolveFormat(jsonOutput, false); err != nil {
		return err
	} else if format == formatJSON {
		jsonOutput = true
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	var result fleetVerifyResult
	if err := fleetVerifyGET(cfg, "/api/fleet/health", &result.Health); err != nil {
		return err
	}
	var envelope struct {
		Items []fleetVerifySchedule `json:"items"`
	}
	if err := fleetVerifyGET(cfg, "/api/fleet/schedules/status", &envelope); err != nil {
		return err
	}
	result.Schedules = envelope.Items
	if !result.Health.Complete {
		result.Issues = append(result.Issues, fleetVerifyIssue{"health_unknown", "fleet", "health observation is incomplete"})
	}
	if result.Health.Apps.Degraded > 0 || result.Health.Apps.Crashed > 0 || result.Health.Replicas.Lost > 0 {
		result.Issues = append(result.Issues, fleetVerifyIssue{"serving_health", "fleet", fmt.Sprintf("%d degraded, %d crashed, %d lost replicas", result.Health.Apps.Degraded, result.Health.Apps.Crashed, result.Health.Replicas.Lost)})
	}
	for _, schedule := range result.Schedules {
		resource := schedule.Slug + "/" + schedule.Schedule
		if schedule.ProducerRepairRequired {
			result.Issues = append(result.Issues, fleetVerifyIssue{"producer_repair", resource, "producer write is uncertain and requires repair"})
			continue
		}
		if schedule.Enabled && schedule.FreshnessError != "" {
			result.Issues = append(result.Issues, fleetVerifyIssue{"freshness_unknown", resource, schedule.FreshnessError})
		} else if schedule.Enabled && schedule.Stale != nil && *schedule.Stale {
			result.Issues = append(result.Issues, fleetVerifyIssue{"schedule_stale", resource, "cron freshness is overdue"})
		}
		if schedule.Enabled && schedule.DeployTrigger != "" && schedule.DeployTrigger != "never" && !schedule.DeployTriggerSatisfied {
			detail := fmt.Sprintf("current %s (%s), producer %s (%s), convergence %s", schedule.CurrentAppVersion, shortDigest(schedule.CurrentContentDigest), schedule.ProducerAppVersion, shortDigest(schedule.ProducerContentDigest), schedule.ConvergenceStatus)
			if schedule.ConvergenceError != "" {
				detail += ": " + schedule.ConvergenceError
			}
			result.Issues = append(result.Issues, fleetVerifyIssue{"code_data_mismatch", resource, detail})
		}
	}
	sort.Slice(result.Issues, func(i, j int) bool { return result.Issues[i].Resource < result.Issues[j].Resource })
	result.OK = len(result.Issues) == 0

	out := cmd.OutOrStdout()
	if jsonOutput {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, string(encoded))
	} else {
		state := "PASS"
		if !result.OK {
			state = "FAIL"
		}
		fmt.Fprintf(out, "Fleet verification: %s (read-only)\n", state)
		fmt.Fprintf(out, "Serving: %d running, %d idle, %d degraded, %d crashed; %d lost replicas\n", result.Health.Apps.Running, result.Health.Apps.Idle, result.Health.Apps.Degraded, result.Health.Apps.Crashed, result.Health.Replicas.Lost)
		fmt.Fprintf(out, "Schedules: %d checked\n", len(result.Schedules))
		for _, issue := range result.Issues {
			fmt.Fprintf(out, "  ! %-20s %-28s %s\n", issue.Kind, issue.Resource, issue.Detail)
		}
	}
	if !result.OK {
		return &ExitCodeError{Code: 4, Kind: KindPartialConvergence, Err: fmt.Errorf("fleet verification found %d issue(s)", len(result.Issues)), Reported: true}
	}
	return nil
}
