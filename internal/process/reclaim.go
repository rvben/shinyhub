package process

// reclaimFreed reports whether a cgroup memory reclaim freed enough of a
// container's resident memory to count as "freed" for warm-wake. It returns true
// only when the reclaimed fraction of the pre-suspend RSS meets minFraction, so a
// partial reclaim that leaves significant RAM resident is rejected and the caller
// falls back to a full Stop - preserving the never-worse-than-cold-stop invariant
// (today's hibernation frees all of an idle app's RAM).
//
// The bar is a fraction rather than a near-zero absolute because a reclaimed
// interpreter always retains non-swappable pages (page tables, mlocked regions,
// resident text); that bounded residual is the accepted cost of a warm wake.
func reclaimFreed(preRSS, postRSS uint64, minFraction float64) bool {
	if preRSS == 0 || postRSS >= preRSS {
		return false
	}
	freed := float64(preRSS-postRSS) / float64(preRSS)
	return freed >= minFraction
}
