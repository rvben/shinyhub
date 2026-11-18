// Package entitlementpolicy defines the desired and observed fleet policy.
package entitlementpolicy

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxDefinitions = 64
const Manual = "manual"

var ErrOwnership = errors.New("entitlement policy ownership conflict")

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var ownerPattern = regexp.MustCompile(`^fleet:[a-z0-9-]{1,64}$`)

func ValidName(name string) bool   { return namePattern.MatchString(name) }
func ValidOwner(owner string) bool { return ownerPattern.MatchString(owner) }
func ValidDescription(description string) bool {
	return len(description) <= 512 && utf8.ValidString(description)
}
func ValidGroup(group string) bool {
	return group != "" && strings.TrimSpace(group) == group && len(group) <= 512 &&
		utf8.ValidString(group) && strings.IndexFunc(group, unicode.IsControl) < 0
}

// A nil policy makes no declaration. An empty policy clears managed entries.
type Policy map[string]Definition
type Definition struct {
	Description *string  `toml:"description" json:"description,omitempty"`
	Groups      []string `toml:"groups" json:"groups,omitempty"`
	Adopt       bool     `toml:"adopt" json:"adopt,omitempty"`
	AdoptGroups []string `toml:"adopt_groups" json:"adopt_groups,omitempty"`
}

func (p Policy) names() []string {
	names := make([]string, 0, len(p))
	for name := range p {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p Policy) Validate() error {
	if len(p) > MaxDefinitions {
		return fmt.Errorf("at most %d entitlement definitions are allowed", MaxDefinitions)
	}
	totalGroups := 0
	for _, name := range p.names() {
		def := p[name]
		totalGroups += len(def.Groups)
		if !ValidName(name) {
			return fmt.Errorf("invalid entitlement name %q", name)
		}
		if def.Description != nil && !ValidDescription(*def.Description) {
			return fmt.Errorf("entitlement %q: invalid description (maximum 512 UTF-8 bytes)", name)
		}
		if len(def.Groups) > 128 || len(def.AdoptGroups) > len(def.Groups) {
			return fmt.Errorf("entitlement %q: at most 128 groups; adopt_groups must be a subset of groups", name)
		}
		groups := make(map[string]bool, len(def.Groups))
		for _, group := range def.Groups {
			if !ValidGroup(group) || groups[group] {
				return fmt.Errorf("entitlement %q: invalid or duplicate group %q", name, group)
			}
			groups[group] = true
		}
		for _, group := range def.AdoptGroups {
			if !groups[group] {
				return fmt.Errorf("entitlement %q: adopt_groups contains undeclared or duplicate group %q", name, group)
			}
			delete(groups, group)
		}
	}
	if totalGroups > 1024 {
		return fmt.Errorf("at most 1024 group grants may be declared per app")
	}
	return nil
}

type State struct {
	Definitions []StoredDefinition
	Groups      []StoredGroup
}
type StoredDefinition struct {
	Name, Description, Owner string
	DirectCount              int
}
type StoredGroup struct {
	Name, Group, Owner string
}

type Change struct {
	Action      string `json:"action,omitempty"`
	DirectCount int    `json:"direct_grants,omitempty"`
	Name        string `json:"name"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"managed_by,omitempty"`
	Previous    string `json:"previous_description,omitempty"`
}

type Plan struct {
	Changes   []Change `json:"changes"`
	Unmanaged []Change `json:"unmanaged,omitempty"`
}

const (
	Define     = "define"
	Delete     = "delete"
	Grant      = "grant"
	Revoke     = "revoke"
	Adopt      = "adopt"
	Relinquish = "relinquish"
)

func (s State) Preview(p Policy, owner string) (Plan, error) {
	plan := Plan{Changes: []Change{}}
	definitions := make(map[string]StoredDefinition, len(s.Definitions))
	groups := make(map[string]map[string]StoredGroup)
	retained := make(map[string]bool)
	appendChange := func(action, name, group, description, previous, managedBy string) {
		plan.Changes = append(plan.Changes, Change{Action: action, Name: name, Group: group, Description: description, Owner: managedBy, Previous: previous})
	}
	collision := func(name, managedBy string) error {
		return fmt.Errorf("%w: entitlement %q belongs to %s; run apps entitlements relinquish for that fleet before managing it", ErrOwnership, name, managedBy)
	}
	for _, grant := range s.Groups {
		if groups[grant.Name] == nil {
			groups[grant.Name] = map[string]StoredGroup{}
		}
		groups[grant.Name][grant.Group] = grant
		def := p[grant.Name]
		desired := slices.Contains(def.Groups, grant.Group)
		switch {
		case grant.Owner == owner && !desired:
			appendChange(Revoke, grant.Name, grant.Group, "", "", owner)
		case grant.Owner == Manual && desired && slices.Contains(def.AdoptGroups, grant.Group):
			appendChange(Adopt, grant.Name, grant.Group, "", "", owner)
		case grant.Owner != owner:
			if desired && grant.Owner != Manual {
				return Plan{}, collision(grant.Name, grant.Owner)
			}
			retained[grant.Name] = true
			plan.Unmanaged = append(plan.Unmanaged, Change{Name: grant.Name, Group: grant.Group, Owner: grant.Owner})
		}
	}

	count := len(s.Definitions)
	for _, stored := range s.Definitions {
		definitions[stored.Name] = stored
		def, desired := p[stored.Name]
		switch {
		case stored.Owner == owner && !desired:
			if retained[stored.Name] || stored.DirectCount > 0 {
				appendChange(Relinquish, stored.Name, "", stored.Description, "", Manual)
			} else {
				appendChange(Delete, stored.Name, "", "", "", owner)
				count--
			}
		case stored.Owner == owner || (stored.Owner == Manual && desired && def.Adopt):
			description := stored.Description
			if def.Description != nil {
				description = *def.Description
			}
			if stored.Owner == Manual {
				appendChange(Adopt, stored.Name, "", description, stored.Description, owner)
			} else if description != stored.Description {
				appendChange(Define, stored.Name, "", description, stored.Description, owner)
			}
		default:
			if desired && stored.Owner != Manual {
				return Plan{}, collision(stored.Name, stored.Owner)
			}
			if desired && def.Description != nil && *def.Description != stored.Description {
				return Plan{}, fmt.Errorf("entitlement %q has a different manual description; align it with apps entitlements define or set adopt = true", stored.Name)
			}
			plan.Unmanaged = append(plan.Unmanaged, Change{Name: stored.Name, Owner: stored.Owner})
		}
		if stored.DirectCount > 0 {
			plan.Unmanaged = append(plan.Unmanaged, Change{Action: "direct_grants_retained", Name: stored.Name, Owner: Manual, DirectCount: stored.DirectCount})
		}
	}
	for _, name := range p.names() {
		def := p[name]
		if _, exists := definitions[name]; !exists {
			description := ""
			if def.Description != nil {
				description = *def.Description
			}
			appendChange(Define, name, "", description, "", owner)
			count++
		}
		for _, group := range def.Groups {
			if _, exists := groups[name][group]; !exists {
				appendChange(Grant, name, group, "", "", owner)
			}
		}
	}
	if count > MaxDefinitions {
		return Plan{}, fmt.Errorf("policy and manual definitions together would exceed the limit of %d entitlements", MaxDefinitions)
	}
	return plan, nil
}

type Preview struct {
	Plan
	AppID    int64  `json:"app_id"`
	Token    string `json:"token,omitempty"`
	Retained bool   `json:"managed_policy_retained,omitempty"`
}

func (s State) HasOwner(owner string) bool {
	return slices.ContainsFunc(s.Definitions, func(d StoredDefinition) bool { return d.Owner == owner }) ||
		slices.ContainsFunc(s.Groups, func(g StoredGroup) bool { return g.Owner == owner })
}
