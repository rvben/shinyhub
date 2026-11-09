package proxy

// AvailabilityRoute observes the fresh-request route for a published release.
// It neither allocates workers nor changes activity, affinity, or pool state.
// Retained and draining generations cannot make a new session available.
func (p *Proxy) AvailabilityRoute(slug string, deploymentID int64) (serving, activationEligible bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil || pool.settingsDraining || deploymentID == 0 ||
		(pool.activeDeploymentID != 0 && pool.activeDeploymentID != deploymentID) {
		return false, false
	}
	if poolIsElastic(pool) {
		// A worker may be full or bound to another client. Its mere presence
		// doesn't prove a fresh session can use it; report activation eligibility.
		return false, true
	}
	for _, rep := range pool.replicas {
		if rep != nil && !rep.draining.Load() && (rep.deploymentID == 0 || rep.deploymentID == deploymentID) {
			return true, false
		}
	}
	return false, false
}
