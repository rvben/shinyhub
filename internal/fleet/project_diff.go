package fleet

// ObservedProject is the subset of GET /api/projects the project diff needs.
// The CLI maps the API payload into this; the diff stays I/O-free.
//
// The fields are pointers for the same reason ObservedApp's are: nil means
// "not observed", while a non-nil "" is a real stored value that a declared ""
// matches without drift.
type ObservedProject struct {
	Slug        string
	Name        *string
	Description *string
	IconEmoji   *string
}

// ProjectDiff is the planned action for one project. Deliberately not an
// AppDiff: a project has no bundle, no digest, and is never adopted or pruned.
type ProjectDiff struct {
	Slug   string
	Action Action // ActionCreate | ActionUpdateConfig | ActionUnchanged
	// Drift lists the declared keys (name, description, icon) whose observed
	// value differs. On a create it lists every declared key, so the apply can
	// build a fully-named POST body and the plan shows what the create sets.
	Drift []ConfigDriftItem
}

// DiffProjects computes the reconcile plan for the manifest's [[project]]
// blocks. Pure and order-independent, mirroring Diff; it returns manifest
// projects in manifest order.
//
// It never returns a delete row. A fleet manifest may legitimately manage a
// subset of a server's apps, so a project it does not declare can still be
// referenced by apps outside its scope, and deleting the row would strip
// display names from apps the manifest does not own. Removal is
// `shinyhub projects rm`, which refuses while apps still reference it.
func DiffProjects(m *Manifest, observed []ObservedProject) []ProjectDiff {
	obs := make(map[string]ObservedProject, len(observed))
	for _, o := range observed {
		obs[o.Slug] = o
	}

	out := make([]ProjectDiff, 0, len(m.Projects))
	for _, p := range m.Projects {
		d := ProjectDiff{Slug: p.Slug}
		o, present := obs[p.Slug]
		if !present {
			// A zero ObservedProject has nil everywhere, so every declared key
			// asserts. This mirrors DeclaredConfig for apps.
			d.Action = ActionCreate
			d.Drift = projectDrift(p, ObservedProject{})
			out = append(out, d)
			continue
		}
		d.Drift = projectDrift(p, o)
		if len(d.Drift) > 0 {
			d.Action = ActionUpdateConfig
		} else {
			d.Action = ActionUnchanged
		}
		out = append(out, d)
	}
	return out
}

// projectDrift returns the declared keys whose observed value differs. The key
// is "icon", matching the manifest key the operator wrote, even though the API
// field is icon_emoji; the apply layer maps it.
func projectDrift(p ProjectEntry, o ObservedProject) []ConfigDriftItem {
	var d []ConfigDriftItem
	d = appendStringDrift(d, "name", p.Name, o.Name)
	d = appendStringDrift(d, "description", p.Description, o.Description)
	d = appendStringDrift(d, "icon", p.Icon, o.IconEmoji)
	return d
}
