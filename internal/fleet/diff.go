package fleet

import (
	"fmt"
	"sort"
)

// Action is the reconcile verb for one app.
type Action string

const (
	ActionCreate             Action = "create"
	ActionAdopt              Action = "adopt"
	ActionUpdateSource       Action = "update(source)"
	ActionUpdateConfig       Action = "update(config)"
	ActionUpdateSourceConfig Action = "update(source+config)"
	ActionUnchanged          Action = "unchanged"
	ActionDelete             Action = "delete"
)

// ObservedApp is the subset of GET /api/apps the diff needs. The CLI maps the
// API payload into this; the diff stays I/O-free and API-shape-agnostic.
type ObservedApp struct {
	Slug string
	// Name and Description are the server's stored display metadata. They are
	// pointers for the same reason the scalars below are: nil means "not
	// observed" (as in DeclaredConfig's zero ObservedApp) and always asserts a
	// declared value, while a non-nil "" is a real stored value - an app with no
	// description - that a declared "" matches without drift.
	Name        *string
	Description *string
	Icon        *string
	// ProjectSlug is the server's stored project. Pointer-typed for the same
	// reason as Name and Description: nil means "not observed" and always
	// asserts a declared value, while a non-nil "" is a real stored value (an
	// ungrouped app) that a declared "" matches without drift.
	ProjectSlug                  *string
	Access                       string
	HibernateTimeoutMinutes      *int
	Replicas                     *int
	MaxSessionsPerReplica        *int
	RenderSeconds                *float64
	IdentityHeaders              *bool
	MinWarmReplicas              *int
	MemoryLimitMB                *int
	CPUQuotaPercent              *int
	WorkerIsolation              *string
	WorkerGroupedSize            *int
	WorkerMaxWorkers             *int
	WorkerMaxSessionLifetimeSecs *int
	ContentDigest                string
	ManagedBy                    *string

	// Autoscale is the server's stored policy, or nil when not observed (as in
	// DeclaredConfig's zero ObservedApp). nil is distinct from an all-zero
	// (off) policy: it makes a declared block always assert on create - so an
	// explicit `autoscale = { enabled = false }` still wins over a bundle that
	// enables autoscale. This mirrors the *int scalar fields above.
	Autoscale *ObservedAutoscale
}

// ObservedAutoscale is the server's stored autoscale policy.
type ObservedAutoscale struct {
	Enabled     bool
	MinReplicas int
	MaxReplicas int
	Target      float64
}

// ConfigDriftItem is one fleet-declared key whose server value differs from
// the desired value. Values are rendered as strings for stable display.
type ConfigDriftItem struct {
	Key     string
	Server  string
	Desired string
}

// AppDiff is the planned action for one app.
type AppDiff struct {
	Slug          string
	Action        Action
	Owned         bool
	LocalDigest   string
	ServerDigest  string
	ConfigDrift   []ConfigDriftItem
	AdoptRequired bool
	// AdoptFrom is the current owner marker ("fleet:<id>") when this adopt
	// would transfer the app away from a DIFFERENT fleet. Empty for a
	// genuinely unmanaged app (no prior owner) and for non-adopt actions.
	AdoptFrom     string
	PruneEligible bool
}

// Diff computes the reconcile plan. localDigests maps slug -> the client-side
// content digest (""/missing => treat as a forced source change so a
// digest-less plan still converges). It is pure and order-independent: the
// returned slice lists manifest apps in manifest order, then owned-but-absent
// delete rows sorted by slug.
func Diff(m *Manifest, localDigests map[string]string, observed []ObservedApp) []AppDiff {
	marker := "fleet:" + m.FleetID
	obs := make(map[string]ObservedApp, len(observed))
	for _, o := range observed {
		obs[o.Slug] = o
	}

	out := make([]AppDiff, 0, len(m.Apps)+4)
	declared := make(map[string]bool, len(m.Apps))

	for _, app := range m.Apps {
		declared[app.Slug] = true
		d := AppDiff{Slug: app.Slug, LocalDigest: localDigests[app.Slug]}

		o, present := obs[app.Slug]
		if !present {
			d.Action = ActionCreate
			out = append(out, d)
			continue
		}
		d.ServerDigest = o.ContentDigest
		owned := o.ManagedBy != nil && *o.ManagedBy == marker
		d.Owned = owned
		if !owned {
			d.Action = ActionAdopt
			d.AdoptRequired = true
			// A non-empty marker that isn't ours means another fleet owns it;
			// adopting transfers ownership. nil/empty means unmanaged.
			if o.ManagedBy != nil && *o.ManagedBy != "" {
				d.AdoptFrom = *o.ManagedBy
			}
			out = append(out, d)
			continue
		}

		srcChanged := d.LocalDigest == "" || o.ContentDigest == "" || d.LocalDigest != o.ContentDigest
		d.ConfigDrift = configDrift(app, o)
		cfgChanged := len(d.ConfigDrift) > 0

		switch {
		case srcChanged && cfgChanged:
			d.Action = ActionUpdateSourceConfig
		case srcChanged:
			d.Action = ActionUpdateSource
		case cfgChanged:
			d.Action = ActionUpdateConfig
		default:
			d.Action = ActionUnchanged
		}
		out = append(out, d)
	}

	// Owned-but-absent => delete (prune candidate). Other-fleet / unmanaged
	// apps absent from the manifest are never our concern.
	var dels []AppDiff
	for _, o := range observed {
		if declared[o.Slug] {
			continue
		}
		if o.ManagedBy == nil || *o.ManagedBy != marker {
			continue
		}
		dels = append(dels, AppDiff{
			Slug: o.Slug, Action: ActionDelete, Owned: true,
			ServerDigest: o.ContentDigest, PruneEligible: true,
		})
	}
	sort.Slice(dels, func(i, j int) bool { return dels[i].Slug < dels[j].Slug })
	return append(out, dels...)
}

// configDrift returns the fleet-declared keys whose observed server value
// differs from the manifest's desired value. Only declared (non-nil) keys are
// compared (drift covers only fleet-declared keys).
func configDrift(app AppEntry, o ObservedApp) []ConfigDriftItem {
	var d []ConfigDriftItem
	c := EffectiveConfig(app)
	if v := app.Visibility; v != "" && v != o.Access && o.Access != "" {
		d = append(d, ConfigDriftItem{Key: "visibility", Server: o.Access, Desired: v})
	}
	d = appendStringDrift(d, "name", c.Name, o.Name)
	d = appendStringDrift(d, "description", c.Description, o.Description)
	d = appendStringDrift(d, "icon", c.Icon, o.Icon)
	d = appendStringDrift(d, "project", c.Project, o.ProjectSlug)
	if c.HibernateResetToDefault {
		if o.HibernateTimeoutMinutes != nil {
			d = append(d, ConfigDriftItem{Key: "hibernate_timeout_minutes", Server: fmt.Sprintf("%d", *o.HibernateTimeoutMinutes), Desired: "(default)"})
		}
	} else {
		d = appendIntDrift(d, "hibernate_timeout_minutes", c.HibernateTimeoutMinutes, o.HibernateTimeoutMinutes)
	}
	d = appendIntDrift(d, "replicas", c.Replicas, o.Replicas)
	d = appendIntDrift(d, "max_sessions_per_replica", c.MaxSessionsPerReplica, o.MaxSessionsPerReplica)
	d = appendFloatDrift(d, "render_seconds", c.RenderSeconds, o.RenderSeconds)
	d = appendBoolDrift(d, "identity_headers", c.IdentityHeaders, o.IdentityHeaders)
	d = appendIntDrift(d, "min_warm_replicas", c.MinWarmReplicas, o.MinWarmReplicas)
	d = appendIntDrift(d, "memory_limit_mb", c.MemoryLimitMB, o.MemoryLimitMB)
	d = appendIntDrift(d, "cpu_quota_percent", c.CPUQuotaPercent, o.CPUQuotaPercent)
	d = appendStringDrift(d, "worker_isolation", c.WorkerIsolation, o.WorkerIsolation)
	d = appendIntDrift(d, "worker_grouped_size", c.WorkerGroupedSize, o.WorkerGroupedSize)
	d = appendIntDrift(d, "worker_max_workers", c.WorkerMaxWorkers, o.WorkerMaxWorkers)
	d = appendIntDrift(d, "worker_max_session_lifetime_secs", c.WorkerMaxSessionLifetimeSecs, o.WorkerMaxSessionLifetimeSecs)
	d = appendAutoscaleDrift(d, c.Autoscale, o.Autoscale)
	return d
}

// appendAutoscaleDrift adds a single "autoscale" drift item when the declared
// policy differs from the observed one. A nil desired (block absent) means "not
// fleet-declared" => no drift. A nil observed (server not observed, as on
// create) always asserts a declared block, so an explicit off policy still wins
// over the bundle. The whole block is one item because autoscale is reconciled
// atomically (all four columns together).
func appendAutoscaleDrift(d []ConfigDriftItem, desired *AutoscaleConfig, o *ObservedAutoscale) []ConfigDriftItem {
	if desired == nil {
		return d
	}
	enabled := desired.Enabled != nil && *desired.Enabled
	if o != nil &&
		enabled == o.Enabled &&
		desired.MinReplicas == o.MinReplicas &&
		desired.MaxReplicas == o.MaxReplicas &&
		desired.Target == o.Target {
		return d
	}
	server := "(unset)"
	if o != nil {
		server = autoscaleDisplay(o.Enabled, o.MinReplicas, o.MaxReplicas, o.Target)
	}
	return append(d, ConfigDriftItem{
		Key:     "autoscale",
		Server:  server,
		Desired: autoscaleDisplay(enabled, desired.MinReplicas, desired.MaxReplicas, desired.Target),
	})
}

// autoscaleDisplay renders an autoscale policy as a compact drift string:
// "off" when disabled with no remembered bounds, "off (min-max)" when disabled
// but carrying bounds (so a bounds-only change is not shown as "off -> off"),
// else "on(min-max @ target)" with target as a two-decimal fraction, or
// "@ default" when target is 0 (inherit the runtime default).
func autoscaleDisplay(enabled bool, min, max int, target float64) string {
	if !enabled {
		if min == 0 && max == 0 && target == 0 {
			return "off"
		}
		if target > 0 {
			return fmt.Sprintf("off (%d-%d @ %.2f)", min, max, target)
		}
		return fmt.Sprintf("off (%d-%d)", min, max)
	}
	if target > 0 {
		return fmt.Sprintf("on(%d-%d @ %.2f)", min, max, target)
	}
	return fmt.Sprintf("on(%d-%d @ default)", min, max)
}

// DeclaredConfig returns the manifest's declared numeric config for an app as
// drift items against an unset server, suitable for applying to a freshly
// created app (which starts at server defaults). Visibility is excluded because
// the deploy that creates the app already sets it.
func DeclaredConfig(app AppEntry) []ConfigDriftItem {
	// A create/adopt deploy has just applied the bundle manifest itself. Only
	// the outer fleet config needs a follow-up assertion here; including Bundle
	// would cause an unnecessary second worker/resource redeploy.
	app.Bundle = Config{}
	return configDrift(app, ObservedApp{})
}

// appendIntDrift adds a drift item when a declared (non-nil desired) int
// differs from the server value. A nil desired means "not declared" => no
// drift. A nil server value with a declared desired is drift (server unknown
// => will be set).
func appendIntDrift(d []ConfigDriftItem, key string, desired, server *int) []ConfigDriftItem {
	if desired == nil {
		return d
	}
	if server != nil && *server == *desired {
		return d
	}
	srv := "(unset)"
	if server != nil {
		srv = fmt.Sprintf("%d", *server)
	}
	return append(d, ConfigDriftItem{Key: key, Server: srv, Desired: fmt.Sprintf("%d", *desired)})
}

// appendStringDrift is appendIntDrift for a declared string key (name,
// description). Values are quoted so an empty string and leading/trailing
// spaces are visible in the plan output rather than rendering as nothing.
func appendStringDrift(d []ConfigDriftItem, key string, desired, server *string) []ConfigDriftItem {
	if desired == nil {
		return d
	}
	if server != nil && *server == *desired {
		return d
	}
	srv := "(unset)"
	if server != nil {
		srv = fmt.Sprintf("%q", *server)
	}
	return append(d, ConfigDriftItem{Key: key, Server: srv, Desired: fmt.Sprintf("%q", *desired)})
}

func appendFloatDrift(d []ConfigDriftItem, key string, desired, server *float64) []ConfigDriftItem {
	if desired == nil || (server != nil && *server == *desired) {
		return d
	}
	srv := "(unset)"
	if server != nil {
		srv = fmt.Sprintf("%g", *server)
	}
	return append(d, ConfigDriftItem{Key: key, Server: srv, Desired: fmt.Sprintf("%g", *desired)})
}

func appendBoolDrift(d []ConfigDriftItem, key string, desired, server *bool) []ConfigDriftItem {
	if desired == nil || (server != nil && *server == *desired) {
		return d
	}
	srv := "(unset)"
	if server != nil {
		srv = fmt.Sprintf("%t", *server)
	}
	return append(d, ConfigDriftItem{Key: key, Server: srv, Desired: fmt.Sprintf("%t", *desired)})
}
