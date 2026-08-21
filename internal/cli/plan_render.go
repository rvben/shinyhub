package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

const defaultPlanWidth = 80

type planRenderOptions struct {
	Width   int
	Details bool
	Styler  *styler
}

func planOutputWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok && isTTY(f) {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return defaultPlanWidth
}

func planStyler(out io.Writer, override *styler) styler {
	if override != nil {
		return *override
	}
	return stylerFor(out)
}

func renderDeploymentPlanWith(out io.Writer, plan deploymentPlan, opts planRenderOptions) {
	if opts.Width <= 0 {
		opts.Width = defaultPlanWidth
	}
	s := planStyler(out, opts.Styler)
	model := plan.Plan
	if model.SchemaVersion == 0 {
		model = deploymentPlanDocument(plan)
	}

	lines := singlePlanHeaderLines(s, plan, model, opts.Width)
	lines = append(lines, "")
	lines = append(lines, singlePlanImpactLines(s, model, opts.Width)...)
	lines = append(lines, "", "Changes")
	if len(model.Resources) > 0 {
		lines = append(lines, planChangeLines(s, model.Resources[0].Changes, opts.Width)...)
	}

	lines = append(lines, "", "Bundle")
	if opts.Width < 60 {
		lines = append(lines,
			fmt.Sprintf("  Files   %d", plan.Bundle.FileCount),
			"  Source  "+humanBytes(plan.Bundle.UncompressedBytes),
			"  Upload  "+humanBytes(int64(plan.Bundle.CompressedBytes)),
			"  Digest  "+shortDigest(plan.Bundle.Digest),
		)
	} else {
		lines = append(lines, fmt.Sprintf("  %d files%s%s source%s%s upload%s%s",
			plan.Bundle.FileCount, planSeparator(s), humanBytes(plan.Bundle.UncompressedBytes),
			planSeparator(s), humanBytes(int64(plan.Bundle.CompressedBytes)), planSeparator(s), shortDigest(plan.Bundle.Digest)))
	}
	if !opts.Details {
		if plan.SavedPlan != nil {
			lines = append(lines, wrapPlanValue("  Details: ", "shinyhub plan show --details "+shellQuote(plan.SavedPlan.Path), opts.Width)...)
		} else {
			lines = append(lines, wrapPlanValue("  Details: ", "rerun with --details (or use --output json)", opts.Width)...)
		}
	}

	c := model.Counts
	lines = append(lines, "")
	lines = append(lines, planCountsLines(s, c, opts.Width)...)
	if plan.SavedPlan != nil {
		remaining := time.Until(plan.SavedPlan.ExpiresAt).Round(time.Minute)
		if remaining < 0 {
			remaining = 0
		}
		lines = append(lines, planSavedLines(s, plan.SavedPlan, remaining, opts.Width)...)
	}
	if len(model.NextActions) > 0 {
		lines = append(lines, "", "Next:")
		lines = append(lines, "  "+model.NextActions[0].Command)
	}

	if opts.Details {
		lines = append(lines, "")
		lines = append(lines, singlePlanDetailLines(s, plan, opts.Width)...)
	}
	writePlanLines(out, s, opts.Width, lines)
}

func renderFleetPlanHuman(out io.Writer, model planDocument, fleetID, command string, width int, s styler) {
	renderFleetPlanHumanWithBundleFiles(out, model, fleetID, command, width, s, nil)
}

func renderFleetPlanHumanWithBundleFiles(out io.Writer, model planDocument, fleetID, command string, width int, s styler, bundleFiles []jsonBundleFile) {
	if width <= 0 {
		width = defaultPlanWidth
	}
	severity := planSeverityInfo
	if model.Counts.Delete > 0 {
		severity = planSeverityDestructive
	} else if model.Counts.Adopt > 0 {
		severity = planSeverityWarning
	}
	lines := paintPlanLines(s, severity, wrapPlanValue("", model.Outcome, width))
	lines = append(lines, fleetPlanMetaLines(s, fleetID, model.Target, command, width)...)

	ownership := filterPlanResources(model.Resources, planActionAdopt)
	if len(ownership) > 0 {
		lines = append(lines, "", planPaint(s, planSeverityWarning, "Ownership changes"))
		lines = append(lines, fleetResourceLines(s, ownership, width, true)...)
	}

	projects := make([]planResource, 0)
	apps := make([]planResource, 0)
	deletes := make([]planResource, 0, model.Counts.Delete)
	for _, resource := range model.Resources {
		switch resource.Action {
		case planActionDelete:
			deletes = append(deletes, resource)
		case planActionAdopt:
			// Ownership has its own risk section above.
		default:
			if resource.Kind == "project" {
				projects = append(projects, resource)
			} else {
				apps = append(apps, resource)
			}
		}
	}
	lines = append(lines, "", "Changes")
	if len(projects) > 0 {
		lines = append(lines, fmt.Sprintf("Projects (%d)", len(projects)))
		lines = append(lines, fleetResourceLines(s, projects, width, false)...)
	}
	appCount := len(planResourcesByKind(model, "app"))
	if width < 60 {
		lines = append(lines, fmt.Sprintf("Apps (%d)", appCount),
			"  Legend: + create, ~ update",
			"          > adopt, - delete, = ok")
	} else {
		lines = append(lines, fmt.Sprintf("Apps (%d)   legend: %s", appCount, planLegend))
	}
	lines = append(lines, fleetResourceLines(s, apps, width, false)...)
	if len(deletes) > 0 {
		heading := fmt.Sprintf("Deletes (%d) — irreversible; requires --prune and confirmation", len(deletes))
		if s.ascii {
			heading = fmt.Sprintf("Deletes (%d) - irreversible; requires --prune and confirmation", len(deletes))
		}
		lines = append(lines, "")
		if width < 60 {
			shortHeading := fmt.Sprintf("Deletes (%d) — irreversible", len(deletes))
			if s.ascii {
				shortHeading = fmt.Sprintf("Deletes (%d) - irreversible", len(deletes))
			}
			lines = append(lines,
				planPaint(s, planSeverityDestructive, shortHeading),
				planPaint(s, planSeverityDestructive, "  Requires --prune and confirmation."),
			)
		} else {
			lines = append(lines, planPaint(s, planSeverityDestructive, heading))
		}
		lines = append(lines, fleetResourceLines(s, deletes, width, false)...)
	}
	if len(bundleFiles) > 0 {
		lines = append(lines, "", "Shared bundle inputs")
		for _, input := range bundleFiles {
			lines = append(lines, wrapPlanValue("  ", input.From+" -> "+input.To, width)...)
			verb := "have"
			if len(input.PlannedConsumers) == 1 {
				verb = "has"
			}
			summary := fmt.Sprintf("consumers: %d (%d %s a planned source update)",
				len(input.Consumers), len(input.PlannedConsumers), verb)
			lines = append(lines, wrapPlanValue("    ", summary, width)...)
		}
	}

	lines = append(lines, "")
	lines = append(lines, planCountsLines(s, model.Counts, width)...)
	if len(model.NextActions) > 0 {
		next := model.NextActions[0]
		lines = append(lines, "", "Next:")
		lines = append(lines, "  "+next.Command)
		lines = append(lines, wrapPlanValue("  ", next.Description, width)...)
		if next.RequiresConfirmation {
			lines = append(lines, wrapPlanValue("  ", "Confirmation is required; the suggested command never pre-confirms it.", width)...)
		}
	}
	writePlanLines(out, s, width, lines)
}

func fleetPlanMetaLine(s styler, fleetID, target, command string) string {
	if s.ascii {
		return fmt.Sprintf("%s  |  fleet_id=%s  |  server=%s", command, fleetID, target)
	}
	return fmt.Sprintf("%s  ·  fleet_id=%s  ·  server=%s", command, fleetID, target)
}

func fleetPlanMetaLines(s styler, fleetID, target, command string, width int) []string {
	if width >= 60 {
		return []string{fleetPlanMetaLine(s, fleetID, target, command)}
	}
	return []string{command, "fleet_id: " + fleetID, "server:   " + target}
}

func filterPlanResources(resources []planResource, action planAction) []planResource {
	result := make([]planResource, 0)
	for _, resource := range resources {
		if resource.Action == action {
			result = append(result, resource)
		}
	}
	return result
}

func fleetResourceLines(s styler, resources []planResource, width int, ownership bool) []string {
	if width < 96 {
		lines := make([]string, 0, len(resources)*2)
		for _, resource := range resources {
			glyph, word := planActionGlyphWord(resource.Action)
			name := resource.Kind + "." + resource.Name
			base := fmt.Sprintf("  %s %-9s %s", planActionText(s, resource.Action, glyph), word, name)
			reasons := fleetResourceReasonLines(s, resource, ownership)
			if len(reasons) > 0 {
				if width < 60 || len(reasons) > 1 {
					lines = append(lines, base)
					for _, reason := range reasons {
						lines = append(lines, wrapPlanValue("      ", reason, width)...)
					}
				} else {
					lines = append(lines, wrapPlanValue(base+"  ", reasons[0], width)...)
				}
			} else {
				lines = append(lines, base)
			}
		}
		return lines
	}
	lines := []string{"  ACTION  RESOURCE                       CHANGE"}
	for _, resource := range resources {
		glyph, _ := planActionGlyphWord(resource.Action)
		name := resource.Kind + "." + resource.Name
		lines = append(lines, fmt.Sprintf("  %s       %-30s %s",
			planActionText(s, resource.Action, glyph), name, fleetResourceReason(s, resource, ownership)))
	}
	return lines
}

func fleetResourceReasonLines(s styler, resource planResource, ownership bool) []string {
	if resource.Action != planActionUpdate || len(resource.Changes) < 2 {
		if reason := fleetResourceReason(s, resource, ownership); reason != "" {
			return []string{reason}
		}
		return nil
	}
	lines := make([]string, 0, len(resource.Changes)+len(resource.Notes))
	for _, change := range resource.Changes {
		one := resource
		one.Changes = []planChange{change}
		one.Notes = nil
		lines = append(lines, fleetResourceReason(s, one, ownership))
	}
	for _, note := range resource.Notes {
		if note != "new" && note != "unchanged" {
			lines = append(lines, note)
		}
	}
	return lines
}

func fleetResourceReason(s styler, resource planResource, ownership bool) string {
	if ownership {
		if owner := planChangeByField(resource.Changes, "owner"); owner != nil {
			current, planned := planHumanChangeValues(*owner)
			return "owner " + current + planArrow(s) + planned
		}
	}
	reason := planResourceReason(resource)
	if !s.ascii {
		reason = strings.ReplaceAll(reason, " -> ", " → ")
	}
	return reason
}

func singlePlanHeaderLines(s styler, plan deploymentPlan, model planDocument, width int) []string {
	lines := wrapPlanValue("", model.Outcome, width)
	if width >= 60 {
		return append(lines, planMetaLine(s, plan, model))
	}
	kind := "read-only"
	if plan.SavedPlan != nil {
		kind = "saved exact plan; no remote changes"
	}
	return append(lines, kind, "server: "+model.Target)
}

func planMetaLine(s styler, plan deploymentPlan, model planDocument) string {
	kind := "read-only"
	if plan.SavedPlan != nil {
		kind = "saved exact plan" + planSeparator(s) + "no remote changes"
	}
	return fmt.Sprintf("%s%s%s", kind, planSeparator(s), model.Target)
}

func singlePlanImpactLines(s styler, model planDocument, width int) []string {
	lines := []string{"Impact"}
	if len(model.Impacts) == 0 && len(model.Warnings) == 0 {
		return append(lines, "  = none")
	}
	for _, impact := range model.Impacts {
		marker := "!"
		if impact.Severity == planSeverityInfo {
			marker = "·"
			if s.ascii {
				marker = "-"
			}
		}
		label := string(impact.Kind)
		prefix := "  " + planPaint(s, impact.Severity, marker+" "+label) + "  "
		lines = append(lines, wrapPlanValue(prefix, impact.Summary, width)...)
	}
	for _, warning := range model.Warnings {
		prefix := "  " + planPaint(s, warning.Severity, "! warning") + "  "
		lines = append(lines, wrapPlanValue(prefix, warning.Summary, width)...)
	}
	return lines
}

func planChangeLines(s styler, changes []planChange, width int) []string {
	if len(changes) == 0 {
		return []string{"  = no field changes"}
	}
	if width < 96 {
		lines := make([]string, 0, len(changes)*3)
		for _, change := range changes {
			glyph, _ := planActionGlyphWord(change.Action)
			current, planned := planHumanChangeValues(change)
			lines = append(lines,
				"  "+planActionText(s, change.Action, glyph)+" "+change.Field,
				"      current  "+current,
				"      planned  "+planned,
			)
		}
		return lines
	}
	lines := []string{"  ACTION  AREA                  CURRENT              PLANNED"}
	for _, change := range changes {
		glyph, _ := planActionGlyphWord(change.Action)
		current, planned := planHumanChangeValues(change)
		lines = append(lines, fmt.Sprintf("  %s       %-20s  %-19s  %s",
			planActionText(s, change.Action, glyph), change.Field, current, planned))
	}
	return lines
}

func planHumanChangeValues(change planChange) (string, string) {
	return planHumanValue(change.Current), planHumanValue(change.Planned)
}

func planHumanValue(v *planValue) string {
	if v == nil {
		return "-"
	}
	if v.Kind == planValueDigest && !v.Unknown {
		return shortDigest(v.Display)
	}
	return v.Display
}

func planCountsLine(s styler, c planCounts) string {
	parts := []string{
		fmt.Sprintf("%d to create", c.Create), fmt.Sprintf("%d to update", c.Update),
		fmt.Sprintf("%d to adopt", c.Adopt), fmt.Sprintf("%d to delete", c.Delete),
		fmt.Sprintf("%d unchanged", c.Unchanged),
	}
	line := "Plan: " + strings.Join(parts, ", ") + "."
	if c.Delete > 0 {
		return s.red(line)
	}
	return line
}

func planCountsLines(s styler, c planCounts, width int) []string {
	if width >= 60 {
		return []string{planCountsLine(s, c)}
	}
	lines := []string{"Plan:"}
	for _, item := range []struct {
		action planAction
		count  int
		label  string
	}{
		{planActionCreate, c.Create, "create"},
		{planActionUpdate, c.Update, "update"},
		{planActionAdopt, c.Adopt, "adopt"},
		{planActionDelete, c.Delete, "delete"},
		{planActionUnchanged, c.Unchanged, "unchanged"},
	} {
		glyph, _ := planActionGlyphWord(item.action)
		lines = append(lines, fmt.Sprintf("  %s %d %s", planActionText(s, item.action, glyph), item.count, item.label))
	}
	return lines
}

func planSavedLine(s styler, saved *savedPlanSummary, remaining time.Duration) string {
	mark := s.glyphOK()
	if !s.tty {
		mark = "+"
	}
	return planPaint(s, planSeverityInfo, mark+" Saved exact plan") +
		fmt.Sprintf("  %s%s%s remaining", saved.Path, planSeparator(s), remaining)
}

func planSavedLines(s styler, saved *savedPlanSummary, remaining time.Duration, width int) []string {
	if width >= 60 {
		return []string{planSavedLine(s, saved, remaining)}
	}
	mark := s.glyphOK()
	if !s.tty {
		mark = "+"
	}
	return []string{
		planPaint(s, planSeverityInfo, mark+" Saved exact plan"),
		"  " + saved.Path,
		fmt.Sprintf("  %s remaining", remaining),
	}
}

func paintPlanLines(s styler, severity planSeverity, lines []string) []string {
	painted := make([]string, len(lines))
	for i, line := range lines {
		painted[i] = planPaint(s, severity, line)
	}
	return painted
}

func singlePlanDetailLines(s styler, plan deploymentPlan, width int) []string {
	lines := []string{"Details"}
	lines = append(lines, wrapPlanValue("  Source      ", plan.Source, width)...)
	lines = append(lines, wrapPlanValue("  Bundle      ", plan.Bundle.Digest, width)...)
	for _, path := range plan.Bundle.Files {
		lines = append(lines, wrapPlanValue("    ", path, width)...)
	}
	if plan.Bundle.IgnoreFile != "" {
		lines = append(lines, wrapPlanValue("  Ignored     ", fmt.Sprintf("%d paths via %s", len(plan.Bundle.IgnoredPaths), plan.Bundle.IgnoreFile), width)...)
	}
	for _, group := range plan.Bundle.ProtectedPaths {
		lines = append(lines, wrapPlanValue("  Protected   ", group.Reason+": "+strings.Join(group.Paths, ", "), width)...)
	}
	lines = append(lines, "", "Launch")
	lines = append(lines, wrapPlanValue("  Runtime     ", plan.Launch.Runtime, width)...)
	lines = append(lines, wrapPlanValue("  Command     ", strings.Join(plan.Launch.Command, " "), width)...)
	if len(plan.Launch.DependencyPreparation) > 0 {
		lines = append(lines, wrapPlanValue("  Prepare     ", strings.Join(plan.Launch.DependencyPreparation, planArrow(s)), width)...)
	}
	readiness := fmt.Sprintf("GET %s%s%s%stimeout %ds", plan.Launch.ReadinessPath, planSeparator(s),
		plan.Launch.ReadinessStatus, planSeparator(s), plan.Launch.StartupTimeoutSeconds)
	lines = append(lines, wrapPlanValue("  Readiness   ", readiness, width)...)
	lines = append(lines, "", "Manifest")
	if !plan.Manifest.Present {
		lines = append(lines, wrapPlanValue("  ", "No shinyhub.toml; server defaults and current settings apply.", width)...)
	} else {
		for _, effect := range plan.Manifest.Effects {
			lines = append(lines, wrapPlanValue("  ", effect, width)...)
		}
	}
	lines = append(lines, "", "Target")
	lines = append(lines, wrapPlanValue("  App         ", plan.Slug, width)...)
	lines = append(lines, wrapPlanValue("  URL         ", plan.AppURL, width)...)
	lines = append(lines, wrapPlanValue("  Permission  ", plan.Permission, width)...)
	lines = append(lines, wrapPlanValue("  Visibility  ", plan.Visibility, width)...)
	if plan.SavedPlan != nil {
		lines = append(lines, "", "Saved plan metadata")
		lines = append(lines, wrapPlanValue("  Plan ID     ", plan.SavedPlan.PlanID, width)...)
		lines = append(lines, wrapPlanValue("  Expires     ", plan.SavedPlan.ExpiresAt.Format(time.RFC3339), width)...)
		lines = append(lines, wrapPlanValue("  Integrity   ", plan.SavedPlan.Integrity, width)...)
		lines = append(lines, wrapPlanValue("  ", "Contains application source; keep private and do not commit.", width)...)
	}
	return lines
}

func planArrow(s styler) string {
	if s.ascii {
		return " -> "
	}
	return " → "
}

func planSeparator(s styler) string {
	if s.ascii {
		return " | "
	}
	return " · "
}

func planActionText(s styler, action planAction, text string) string {
	switch action {
	case planActionCreate:
		return s.green(text)
	case planActionUpdate, planActionAdopt:
		return s.yellow(text)
	case planActionDelete:
		return s.red(text)
	case planActionUnchanged:
		return s.dim(text)
	default:
		return text
	}
}

func planPaint(s styler, severity planSeverity, text string) string {
	switch severity {
	case planSeverityDestructive:
		return s.red(text)
	case planSeverityWarning:
		return s.yellow(text)
	default:
		return text
	}
}

func wrapPlanValue(prefix, value string, width int) []string {
	if width <= 0 || visibleWidth(prefix)+visibleWidth(value) <= width {
		return []string{prefix + value}
	}
	available := width - visibleWidth(prefix)
	if available < 16 {
		available = 16
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{prefix}
	}
	indent := strings.Repeat(" ", visibleWidth(prefix))
	lines, current := []string{}, prefix
	used := 0
	for _, word := range words {
		if visibleWidth(word) > available {
			if used > 0 {
				lines = append(lines, current)
			}
			chunks := splitPlanToken(word, available)
			for _, chunk := range chunks[:len(chunks)-1] {
				lines = append(lines, indent+chunk)
			}
			current = indent + chunks[len(chunks)-1]
			used = visibleWidth(chunks[len(chunks)-1])
			continue
		}
		needed := visibleWidth(word)
		if used > 0 {
			needed++
		}
		if used > 0 && used+needed > available {
			lines = append(lines, current)
			current, used = indent+word, visibleWidth(word)
			continue
		}
		if used > 0 {
			current += " "
			used++
		}
		current += word
		used += visibleWidth(word)
	}
	return append(lines, current)
}

func splitPlanToken(token string, width int) []string {
	if width <= 0 || visibleWidth(token) <= width {
		return []string{token}
	}
	runes := []rune(token)
	chunks := make([]string, 0, (len(runes)+width-1)/width)
	for len(runes) > 0 {
		n := width
		if n > len(runes) {
			n = len(runes)
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}

func writePlanLines(out io.Writer, s styler, width int, lines []string) {
	for _, line := range lines {
		// Keep suggested commands as one copy-pastable shell line. A terminal may
		// soft-wrap an unusually long path, but inserting a hard newline would
		// change argv and turn the safest next action into a broken command.
		if strings.HasPrefix(strings.TrimSpace(line), "shinyhub ") {
			fmt.Fprintln(out, line)
			continue
		}
		fmt.Fprintln(out, truncateVisible(line, width, s.glyphEllipsis()))
	}
}
