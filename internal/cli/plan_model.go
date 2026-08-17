package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rvben/shinyhub/internal/fleet"
)

// planModelSchemaVersion versions the shared semantic plan model embedded in
// every human and JSON plan. Existing command-specific JSON fields remain
// additive compatibility aliases until a future major schema removes them.
const planModelSchemaVersion = 1

// planAction is the canonical action vocabulary. Renderers and JSON writers
// consume these values directly; they never recover an action from prose.
type planAction string

const (
	planActionCreate    planAction = "create"
	planActionUpdate    planAction = "update"
	planActionAdopt     planAction = "adopt"
	planActionDelete    planAction = "delete"
	planActionUnchanged planAction = "unchanged"
)

type planValueKind string

const (
	planValueString planValueKind = "string"
	planValueDigest planValueKind = "digest"
	planValueAbsent planValueKind = "absent"
)

// planValue keeps display text separate from classification. Sensitive values
// can therefore be redacted by every renderer without inspecting their keys,
// and unknown is distinct from an empty or absent value.
type planValue struct {
	Kind      planValueKind `json:"kind"`
	Display   string        `json:"display"`
	Unknown   bool          `json:"unknown,omitempty"`
	Sensitive bool          `json:"sensitive,omitempty"`
}

type planChange struct {
	Field   string     `json:"field"`
	Action  planAction `json:"action"`
	Current *planValue `json:"current,omitempty"`
	Planned *planValue `json:"planned,omitempty"`
}

type planImpactKind string

const (
	planImpactAvailability planImpactKind = "availability"
	planImpactLifecycle    planImpactKind = "lifecycle"
	planImpactOwnership    planImpactKind = "ownership"
	planImpactDestructive  planImpactKind = "destructive"
)

type planSeverity string

const (
	planSeverityInfo        planSeverity = "info"
	planSeverityWarning     planSeverity = "warning"
	planSeverityDestructive planSeverity = "destructive"
)

type planImpact struct {
	Kind     planImpactKind `json:"kind"`
	Severity planSeverity   `json:"severity"`
	Summary  string         `json:"summary"`
}

type planNotice struct {
	Severity planSeverity `json:"severity"`
	Summary  string       `json:"summary"`
}

type planResource struct {
	Kind    string       `json:"kind"`
	Name    string       `json:"name"`
	Action  planAction   `json:"action"`
	Changes []planChange `json:"changes"`
	Impacts []planImpact `json:"impacts"`
	Notes   []string     `json:"notes"`
}

// planCounts is the only count model used by plan renderers and JSON summaries.
// Redeploying unchanged content is intentionally an update: it still creates a
// deployment record and can restart replicas.
type planCounts struct {
	Create    int `json:"create"`
	Update    int `json:"update"`
	Adopt     int `json:"adopt"`
	Delete    int `json:"delete"`
	Unchanged int `json:"unchanged"`
}

func (c *planCounts) add(action planAction) {
	switch action {
	case planActionCreate:
		c.Create++
	case planActionUpdate:
		c.Update++
	case planActionAdopt:
		c.Adopt++
	case planActionDelete:
		c.Delete++
	case planActionUnchanged:
		c.Unchanged++
	}
}

func (c planCounts) pending() bool { return c.Create+c.Update+c.Adopt+c.Delete > 0 }

func countsFromPlanResources(resources []planResource) planCounts {
	var counts planCounts
	for _, resource := range resources {
		counts.add(resource.Action)
	}
	return counts
}

type planNextAction struct {
	Command              string `json:"command"`
	Description          string `json:"description"`
	RequiresConfirmation bool   `json:"requires_confirmation"`
}

// planDocument is the shared, renderer-independent contract for single-app and
// fleet plans. Command-specific envelopes embed it under "plan" while retaining
// their existing fields for backwards compatibility.
type planDocument struct {
	SchemaVersion int              `json:"schema_version"`
	Scope         string           `json:"scope"`
	Command       string           `json:"command"`
	Target        string           `json:"target"`
	Outcome       string           `json:"outcome"`
	Resources     []planResource   `json:"resources"`
	Impacts       []planImpact     `json:"impacts"`
	Warnings      []planNotice     `json:"warnings"`
	Counts        planCounts       `json:"counts"`
	NextActions   []planNextAction `json:"next_actions"`
}

func newPlanDocument(scope, command, target, outcome string, resources []planResource, impacts []planImpact, warnings []planNotice, next []planNextAction) planDocument {
	resources = append([]planResource(nil), resources...)
	sort.SliceStable(resources, func(i, j int) bool {
		if resources[i].Kind != resources[j].Kind {
			return planResourceKindRank(resources[i].Kind) < planResourceKindRank(resources[j].Kind)
		}
		if planActionRank(resources[i].Action) != planActionRank(resources[j].Action) {
			return planActionRank(resources[i].Action) < planActionRank(resources[j].Action)
		}
		return resources[i].Name < resources[j].Name
	})
	return planDocument{
		SchemaVersion: planModelSchemaVersion,
		Scope:         scope,
		Command:       command,
		Target:        target,
		Outcome:       outcome,
		Resources:     resources,
		Impacts:       nonNilImpacts(impacts),
		Warnings:      nonNilNotices(warnings),
		Counts:        countsFromPlanResources(resources),
		NextActions:   nonNilNextActions(next),
	}
}

func planResourceKindRank(kind string) int {
	switch kind {
	case "project":
		return 0
	case "app":
		return 1
	default:
		return 2
	}
}

func planActionRank(action planAction) int {
	switch action {
	case planActionCreate:
		return 0
	case planActionUpdate:
		return 1
	case planActionAdopt:
		return 2
	case planActionDelete:
		return 3
	case planActionUnchanged:
		return 4
	default:
		return 5
	}
}

func nonNilImpacts(v []planImpact) []planImpact {
	if v == nil {
		return []planImpact{}
	}
	return v
}

func nonNilNotices(v []planNotice) []planNotice {
	if v == nil {
		return []planNotice{}
	}
	return v
}

func nonNilNextActions(v []planNextAction) []planNextAction {
	if v == nil {
		return []planNextAction{}
	}
	return v
}

func value(kind planValueKind, display string) *planValue {
	return &planValue{Kind: kind, Display: display}
}

func unknownValue(kind planValueKind, display string) *planValue {
	return &planValue{Kind: kind, Display: display, Unknown: true}
}

func deploymentPlanDocument(plan deploymentPlan) planDocument {
	action := planActionUpdate
	switch plan.Action {
	case "create":
		action = planActionCreate
	case "update", "redeploy":
		action = planActionUpdate
	}

	currentContent := value(planValueAbsent, "absent")
	if plan.Remote.Exists {
		if plan.Remote.ContentDigest == "" {
			currentContent = unknownValue(planValueDigest, "unknown")
		} else {
			currentContent = value(planValueDigest, plan.Remote.ContentDigest)
		}
	}
	plannedContent := value(planValueDigest, plan.Bundle.Digest)
	contentAction := action
	if plan.ChangeStatus == "unchanged" {
		contentAction = planActionUnchanged
	}

	changes := []planChange{{Field: "content", Action: contentAction, Current: currentContent, Planned: plannedContent}}
	if !plan.Remote.Exists || plan.Remote.Access != plan.Visibility {
		currentVisibility := value(planValueAbsent, "absent")
		if plan.Remote.Exists {
			if plan.Remote.Access == "" {
				currentVisibility = unknownValue(planValueString, "unknown")
			} else {
				currentVisibility = value(planValueString, plan.Remote.Access)
			}
		}
		changes = append(changes, planChange{
			Field: "visibility", Action: action,
			Current: currentVisibility, Planned: value(planValueString, plan.Visibility),
		})
	}

	severity := planSeverityInfo
	impactKind := planImpactLifecycle
	if plan.Remote.Exists && plan.Remote.Status != "stopped" {
		severity = planSeverityWarning
		impactKind = planImpactAvailability
	}
	impact := planImpact{Kind: impactKind, Severity: severity, Summary: plan.Lifecycle}
	resource := planResource{
		Kind: "app", Name: plan.Slug, Action: action,
		Changes: changes, Impacts: []planImpact{impact}, Notes: []string{},
	}

	warnings := make([]planNotice, 0, len(plan.Warnings))
	for _, warning := range plan.Warnings {
		warnings = append(warnings, planNotice{Severity: planSeverityWarning, Summary: warning})
	}
	verb := string(action)
	if plan.Action == "redeploy" {
		verb = "redeploy"
	}
	outcome := fmt.Sprintf("ShinyHub will %s %s", verb, plan.Slug)
	next := []planNextAction{{
		Command: plan.DeployCommand, Description: "Apply this preview from the selected source", RequiresConfirmation: false,
	}}
	return newPlanDocument("single-app", "shinyhub plan", plan.Host, outcome, []planResource{resource}, []planImpact{impact}, warnings, next)
}

func canonicalFleetAction(action fleet.Action) planAction {
	switch action {
	case fleet.ActionCreate:
		return planActionCreate
	case fleet.ActionUpdateSource, fleet.ActionUpdateConfig, fleet.ActionUpdateSourceConfig:
		return planActionUpdate
	case fleet.ActionAdopt:
		return planActionAdopt
	case fleet.ActionDelete:
		return planActionDelete
	case fleet.ActionUnchanged:
		return planActionUnchanged
	default:
		return planActionUnchanged
	}
}

func fleetProjectPlanResource(project fleet.ProjectDiff) planResource {
	action := canonicalFleetAction(project.Action)
	changes := make([]planChange, 0, len(project.Drift))
	for _, drift := range project.Drift {
		changes = append(changes, planChange{
			Field: drift.Key, Action: action,
			Current: value(planValueString, drift.Server), Planned: value(planValueString, drift.Desired),
		})
	}
	return planResource{Kind: "project", Name: project.Slug, Action: action, Changes: changes, Impacts: []planImpact{}, Notes: []string{}}
}

func fleetAppPlanResource(app fleet.AppDiff, fleetID string) planResource {
	action := canonicalFleetAction(app.Action)
	resource := planResource{Kind: "app", Name: app.Slug, Action: action, Changes: []planChange{}, Impacts: []planImpact{}, Notes: []string{}}

	if app.Action == fleet.ActionUpdateSource || app.Action == fleet.ActionUpdateSourceConfig {
		current := value(planValueDigest, app.ServerDigest)
		if app.ServerDigest == "" {
			current = value(planValueAbsent, "(none)")
		}
		resource.Changes = append(resource.Changes, planChange{
			Field: "content", Action: planActionUpdate,
			Current: current, Planned: value(planValueDigest, app.LocalDigest),
		})
	}
	if app.Action == fleet.ActionUpdateConfig || app.Action == fleet.ActionUpdateSourceConfig {
		for _, drift := range app.ConfigDrift {
			resource.Changes = append(resource.Changes, planChange{
				Field: drift.Key, Action: planActionUpdate,
				Current: value(planValueString, drift.Server), Planned: value(planValueString, drift.Desired),
			})
		}
	}
	if app.Action == fleet.ActionAdopt {
		currentOwner := "unmanaged"
		if app.AdoptFrom != "" {
			currentOwner = app.AdoptFrom
		}
		plannedOwner := "this fleet"
		if fleetID != "" {
			plannedOwner = "fleet:" + fleetID
		}
		resource.Changes = append(resource.Changes, planChange{
			Field: "owner", Action: planActionAdopt,
			Current: value(planValueString, currentOwner), Planned: value(planValueString, plannedOwner),
		})
		impact := planImpact{Kind: planImpactOwnership, Severity: planSeverityWarning, Summary: "ownership will transfer to this fleet"}
		if app.AdoptFrom == "" {
			impact.Summary = "unmanaged app will become fleet-managed"
		}
		resource.Impacts = append(resource.Impacts, impact)
	}
	if app.Action == fleet.ActionDelete {
		resource.Impacts = append(resource.Impacts, planImpact{
			Kind: planImpactDestructive, Severity: planSeverityDestructive,
			Summary: "fleet-managed app is absent from the manifest and eligible for pruning",
		})
	}
	if app.Action == fleet.ActionCreate {
		resource.Notes = append(resource.Notes, "new")
	}
	if app.Action == fleet.ActionUnchanged {
		resource.Notes = append(resource.Notes, "unchanged")
	}
	for _, unmanaged := range app.Unmanaged {
		resource.Notes = append(resource.Notes, fmt.Sprintf("unmanaged: %s=%s (default %s)", unmanaged.Key, unmanaged.Server, unmanaged.Default))
	}
	return resource
}

func fleetPlanDocument(command, file string, manifest *fleet.Manifest, host string, diff []fleet.AppDiff, projects []fleet.ProjectDiff) planDocument {
	resources := make([]planResource, 0, len(projects)+len(diff))
	for _, project := range projects {
		resources = append(resources, fleetProjectPlanResource(project))
	}
	for _, app := range diff {
		resources = append(resources, fleetAppPlanResource(app, manifest.FleetID))
	}

	impacts := []planImpact{}
	for _, resource := range resources {
		impacts = append(impacts, resource.Impacts...)
	}
	doc := newPlanDocument("fleet", command, host, "", resources, impacts, nil, nil)
	doc.Outcome = fleetPlanOutcome(doc.Counts)
	if doc.Counts.pending() {
		applyCommand, description := applySuggestion(file, doc.Counts)
		doc.NextActions = []planNextAction{{
			Command: applyCommand, Description: description,
			RequiresConfirmation: doc.Counts.Adopt > 0 || doc.Counts.Delete > 0,
		}}
	}
	return doc
}

func fleetPlanOutcome(counts planCounts) string {
	changed := counts.Create + counts.Update + counts.Adopt
	switch {
	case !counts.pending():
		return "ShinyHub found no fleet changes"
	case counts.Delete > 0:
		return fmt.Sprintf("ShinyHub will change %s and delete %s", plural(changed, "resource"), plural(counts.Delete, "app"))
	default:
		return fmt.Sprintf("ShinyHub will change %s", plural(changed, "resource"))
	}
}

func planResourcesByKind(doc planDocument, kind string) []planResource {
	resources := make([]planResource, 0)
	for _, resource := range doc.Resources {
		if resource.Kind == kind {
			resources = append(resources, resource)
		}
	}
	return resources
}

func planResourceReason(resource planResource) string {
	var parts []string
	switch resource.Action {
	case planActionCreate:
		parts = append(parts, "new")
	case planActionAdopt:
		if owner := planChangeByField(resource.Changes, "owner"); owner != nil && owner.Current != nil {
			if owner.Current.Display == "unmanaged" {
				parts = append(parts, "unmanaged, not owned by this fleet (needs --adopt)")
			} else {
				parts = append(parts, fmt.Sprintf("owned by %s; --adopt will TRANSFER ownership to this fleet", owner.Current.Display))
			}
		}
	case planActionUnchanged:
		parts = append(parts, "unchanged")
	case planActionDelete:
		parts = append(parts, "fleet-managed, absent from manifest (prune candidate)")
	case planActionUpdate:
		for _, change := range resource.Changes {
			current, planned := planChangeValues(change)
			if change.Field == "content" {
				parts = append(parts, fmt.Sprintf("source %s -> %s", shortDigest(current), shortDigest(planned)))
			} else {
				parts = append(parts, fmt.Sprintf("%s %s -> %s", change.Field, current, planned))
			}
		}
	}
	for _, note := range resource.Notes {
		if note == "new" || note == "unchanged" {
			continue
		}
		parts = append(parts, note)
	}
	return strings.Join(parts, "; ")
}

func planChangeByField(changes []planChange, field string) *planChange {
	for i := range changes {
		if changes[i].Field == field {
			return &changes[i]
		}
	}
	return nil
}

func planChangeValues(change planChange) (string, string) {
	current, planned := "", ""
	if change.Current != nil {
		current = change.Current.Display
	}
	if change.Planned != nil {
		planned = change.Planned.Display
	}
	return current, planned
}
