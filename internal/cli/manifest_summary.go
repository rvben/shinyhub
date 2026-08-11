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
	if warn := formatIconShadowWarning(raw); warn != "" {
		lines = append(lines, warn)
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

// formatKeptStoppedNote turns the "kept_stopped" field of a deploy response
// into the line that tells the developer the new version is on the server but
// not serving, or "" when the app is live. Without it a successful-looking
// "Deployed" would be the only output for a deploy nobody can reach, and the
// next question ("how do I bring it up?") is answered in the same line.
func formatKeptStoppedNote(keptStopped bool, slug string) string {
	if !keptStopped {
		return ""
	}
	return fmt.Sprintf("This app is stopped, so the new version is not serving yet.\n"+
		"      Bring it up with: shinyhub apps start %s", slug)
}

// formatIconShadowWarning turns the manifest block of a deploy response into a
// warning that the declared icon is now displayed instead of this app's
// uploaded image, or "" when it is not. The image is retained, so the message
// names the recovery action: the whole point is that the user would otherwise
// not know it still exists. Shaped like formatHooksSkippedWarning (a
// consequence gets its own line) but reads manifest.icon_shadowed_upload
// rather than a root-level field.
func formatIconShadowWarning(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	if shadowed, _ := m["icon_shadowed_upload"].(bool); !shadowed {
		return ""
	}
	app, _ := m["app"].(map[string]any)
	icon, _ := app["icon"].(string)
	return fmt.Sprintf("Note: [app] icon %q is now shown instead of this app's uploaded image.\n"+
		"      The image is still stored. Set icon = \"\" in shinyhub.toml to use it.", icon)
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
		if k == "autoscale" {
			if as, ok := v.(map[string]any); ok {
				parts = append(parts, "autoscale="+formatAutoscaleSummary(as))
				continue
			}
		}
		if f, ok := v.(float64); ok {
			parts = append(parts, fmt.Sprintf("%s=%d", k, int(f)))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, "; ")
}

// formatAutoscaleSummary renders the nested autoscale block from a deploy
// summary as a compact value: "off" when disabled, else "on (min-max @ target)"
// with target as a two-decimal fraction, or "@ default" when target is 0
// (inherit the runtime default).
func formatAutoscaleSummary(as map[string]any) string {
	enabled, _ := as["enabled"].(bool)
	if !enabled {
		return "off"
	}
	min := jsonInt(as["min_replicas"])
	max := jsonInt(as["max_replicas"])
	if target, _ := as["target"].(float64); target > 0 {
		return fmt.Sprintf("on (%d-%d @ %.2f)", min, max, target)
	}
	return fmt.Sprintf("on (%d-%d @ default)", min, max)
}

// jsonInt reads a JSON number (decoded as float64) as an int, or 0 if absent.
func jsonInt(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
