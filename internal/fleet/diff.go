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
	Slug                    string
	Access                  string
	HibernateTimeoutMinutes *int
	Replicas                *int
	MaxSessionsPerReplica   *int
	ContentDigest           string
	ManagedBy               *string
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
	if v := app.Visibility; v != "" && v != o.Access && o.Access != "" {
		d = append(d, ConfigDriftItem{Key: "visibility", Server: o.Access, Desired: v})
	}
	d = appendIntDrift(d, "hibernate_timeout_minutes", app.Config.HibernateTimeoutMinutes, o.HibernateTimeoutMinutes)
	d = appendIntDrift(d, "replicas", app.Config.Replicas, o.Replicas)
	d = appendIntDrift(d, "max_sessions_per_replica", app.Config.MaxSessionsPerReplica, o.MaxSessionsPerReplica)
	return d
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
