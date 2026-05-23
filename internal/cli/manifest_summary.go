package cli

import (
	"fmt"
	"sort"
	"strings"
)

// formatManifestSummary turns the "manifest" field of a deploy response into
// human-friendly lines printed after the standard "Deployed" message. The
// input is whatever json.Unmarshal produced for the field — typically nil
// (no manifest applied) or map[string]any{"app": {...}, "schedules": [...]}.
//
// Returns one line per non-empty section so callers can decide whether to
// print at all (empty slice ⇒ nothing was applied).
func formatManifestSummary(raw any) []string {
	if raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	var lines []string
	if app, ok := m["app"].(map[string]any); ok && len(app) > 0 {
		lines = append(lines, "Applied [app] settings: "+formatAppFields(app))
	}
	if schedules, ok := m["schedules"].([]any); ok && len(schedules) > 0 {
		created, updated := 0, 0
		for _, s := range schedules {
			entry, ok := s.(map[string]any)
			if !ok {
				continue
			}
			switch entry["action"] {
			case "created":
				created++
			case "updated":
				updated++
			}
		}
		lines = append(lines, fmt.Sprintf("Schedules: %d created, %d updated", created, updated))
	}
	return lines
}

// formatHooksSkippedWarning turns the "hooks_skipped" field of a deploy
// response into a developer-facing warning line, or "" when no hooks were
// skipped. Under a container runtime the host has no view of the app's
// environment, so post-deploy hooks do not run; this tells the developer
// instead of leaving the fact only in the server log.
func formatHooksSkippedWarning(raw any) string {
	n, ok := raw.(float64)
	if !ok || n <= 0 {
		return ""
	}
	noun := "hook"
	if int(n) != 1 {
		noun = "hooks"
	}
	return fmt.Sprintf("Warning: %d post-deploy %s skipped under the container runtime; bake setup into the image instead.", int(n), noun)
}

// formatAppFields renders the [app] summary map as `key=value; key=value` in
// a deterministic order so the line is stable across deploys.
func formatAppFields(app map[string]any) string {
	keys := make([]string, 0, len(app))
	for k := range app {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := app[k]
		if v == nil {
			parts = append(parts, k+"=default")
			continue
		}
		if f, ok := v.(float64); ok {
			parts = append(parts, fmt.Sprintf("%s=%d", k, int(f)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, "; ")
}
