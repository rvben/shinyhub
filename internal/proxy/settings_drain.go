package proxy

// BeginSettingsDrain stops new admissions across the active and retiring
// generations while keeping established connections and affinity routable.
// The returned function restores the original flags if stopping is aborted.
func (p *Proxy) BeginSettingsDrain(slug string) func() {
	p.mu.Lock()
	pool := p.pools[slug]
	if pool == nil {
		p.mu.Unlock()
		return func() {}
	}
	previous := pool.settingsDraining
	pool.settingsDraining = true
	flags := make(map[*replicaBackend]bool)
	mark := func(rb *replicaBackend) {
		if rb != nil {
			if _, exists := flags[rb]; exists {
				return
			}
			flags[rb] = rb.draining.Swap(true)
		}
	}
	for _, rb := range pool.replicas {
		mark(rb)
	}
	for _, w := range pool.workers {
		mark(w)
	}
	for _, generation := range pool.drainingGenerations {
		for _, rb := range generation {
			mark(rb)
		}
	}
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.pools[slug] != pool {
			return
		}
		pool.settingsDraining = previous
		for rb, flag := range flags {
			rb.draining.Store(flag)
		}
	}
}

// SettingsDrainComplete counts real in-flight requests and WebSockets in every
// serving generation, including elastic workers whose indices are sparse.
func (p *Proxy) SettingsDrainComplete(slug string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return true
	}
	for _, rb := range pool.replicas {
		if rb != nil && rb.activeConns.Load() > 0 {
			return false
		}
	}
	for _, w := range pool.workers {
		if w.activeConns.Load() > 0 {
			return false
		}
	}
	for _, generation := range pool.drainingGenerations {
		for _, rb := range generation {
			if rb != nil && rb.activeConns.Load() > 0 {
				return false
			}
		}
	}
	return true
}
