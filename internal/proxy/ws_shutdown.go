package proxy

// markWSSessions records an intended worker stop on the connections that are
// still open at this instant. It does not close them. The returned function
// clears marks on surviving connections if the process stop fails.
func (p *Proxy) markWSSessions(slug string, deploymentID int64, replica int, reason string) func() {
	marked := make([]*wsSession, 0)
	for _, conn := range p.conns.snapshot() {
		s := conn.session
		if s == nil {
			continue
		}
		s.mu.Lock()
		matches := s.slug == slug && s.deploymentID == deploymentID && (replica < 0 || s.replica == replica)
		s.mu.Unlock()
		if matches && s.setProxyReason(reason) {
			marked = append(marked, s)
		}
	}
	return func() {
		for _, s := range marked {
			s.clearProxyReason(reason)
		}
	}
}

// MarkElasticWorkerLifetime identifies sessions bound to the worker whose
// lifetime timer fired. The deployment check fences a later worker occupying
// the same slot after a pool replacement.
func (p *Proxy) MarkElasticWorkerLifetime(slug string, slotID int) func() {
	p.mu.RLock()
	var deploymentID int64
	if pool := p.pools[slug]; pool != nil {
		if worker := pool.workers[slotID]; worker != nil {
			deploymentID = worker.deploymentID
		}
	}
	p.mu.RUnlock()
	if deploymentID == 0 {
		return func() {}
	}
	return p.markWSSessions(slug, deploymentID, slotID, "lifetime")
}

// MarkGenerationDrain identifies sockets on an old deployment immediately
// before its processes are stopped. Normal closes during the drain grace
// period have already completed and receive their own observed close cause.
func (p *Proxy) MarkGenerationDrain(slug string, deploymentID int64) func() {
	if deploymentID <= 0 {
		return func() {}
	}
	return p.markWSSessions(slug, deploymentID, -1, "drain")
}
