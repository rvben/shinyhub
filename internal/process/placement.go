package process

import "fmt"

// TierAssignment binds a global replica index to the tier that should run it.
// Indexes are contiguous and unique across the whole app (a single global
// index space); the tier is an attribute, never folded into the index.
type TierAssignment struct {
	Index int
	Tier  string
}

// ExpandPlacement turns a per-tier replica-count map into a deterministic,
// contiguous list of (index, tier) assignments.
//
// When placement is empty, all fallbackReplicas indexes are assigned to
// defaultTier, reproducing single-tier behavior exactly. When placement is
// non-empty, tiers are walked in tierOrder and each is allocated the next
// contiguous block of indexes; tiers with count 0 are skipped. Every tier in
// placement must appear in tierOrder. The resolved total must be at least 1.
func ExpandPlacement(placement map[string]int, tierOrder []string, fallbackReplicas int, defaultTier string) ([]TierAssignment, error) {
	if len(placement) == 0 {
		if fallbackReplicas < 1 {
			return nil, fmt.Errorf("placement: fallback replica count must be >= 1, got %d", fallbackReplicas)
		}
		out := make([]TierAssignment, fallbackReplicas)
		for i := range out {
			out[i] = TierAssignment{Index: i, Tier: defaultTier}
		}
		return out, nil
	}

	known := make(map[string]bool, len(tierOrder))
	for _, name := range tierOrder {
		known[name] = true
	}
	for tier, count := range placement {
		if !known[tier] {
			return nil, fmt.Errorf("placement: tier %q is not a configured tier", tier)
		}
		if count < 0 {
			return nil, fmt.Errorf("placement: tier %q has negative count %d", tier, count)
		}
	}

	var out []TierAssignment
	idx := 0
	for _, tier := range tierOrder {
		for c := 0; c < placement[tier]; c++ {
			out = append(out, TierAssignment{Index: idx, Tier: tier})
			idx++
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("placement: total replica count must be >= 1")
	}
	return out, nil
}
