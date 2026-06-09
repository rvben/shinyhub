package auth

// GroupRoleMapping maps an IdP group name to a ShinyHub global role.
type GroupRoleMapping struct {
	Group string
	Role  string
}

// ResolveGlobalRole returns the highest-rank valid role among mappings whose
// group is present in groups. matched is false when no mapping applies; callers
// then fall back to a default role. Mappings naming an unknown role are ignored.
func ResolveGlobalRole(groups []string, mappings []GroupRoleMapping, defaultRole string) (role string, matched bool) {
	have := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		have[g] = struct{}{}
	}
	bestRank := 0
	for _, m := range mappings {
		if _, ok := have[m.Group]; !ok {
			continue
		}
		if !IsValidGlobalRole(m.Role) {
			continue
		}
		if r := roleOrder[m.Role]; r > bestRank {
			bestRank = r
			role = m.Role
			matched = true
		}
	}
	return role, matched
}
