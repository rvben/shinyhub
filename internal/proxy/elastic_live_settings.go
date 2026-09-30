package proxy

// SetElasticLifetimeUpdateFunc wires live settings updates into lifecycle.
// The callback must not acquire the app operation lock: PATCH already holds it.
func (p *Proxy) SetElasticLifetimeUpdateFunc(fn func(string, int)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updateElasticLifetime = fn
}

func (p *Proxy) ApplyElasticLifetime(slug string, seconds int) {
	p.mu.RLock()
	update := p.updateElasticLifetime
	p.mu.RUnlock()
	if update != nil {
		update(slug, seconds)
	}
}
