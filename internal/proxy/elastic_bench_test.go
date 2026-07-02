package proxy

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// BenchmarkElasticAccounting_Contention measures the per-request elastic
// accounting cost (clientConnOpened + clientConnClosed, the two accounting
// sections the hot path runs via defer) across DISTINCT slugs, one per parallel
// goroutine. With the state partitioned per-client under a shared read lock plus
// cs.mu, this scales with GOMAXPROCS; the pre-scaling code took the global write
// lock here and serialised unrelated apps. Run with -cpu 1,2,4,8 to observe
// scaling.
//
//	GOWORK=off go test ./internal/proxy/ -run '^$' \
//	  -bench BenchmarkElasticAccounting_Contention -cpu 1,2,4,8 -benchtime 2s
func BenchmarkElasticAccounting_Contention(b *testing.B) {
	const nSlugs = 512
	p := New()
	slugs := make([]string, nSlugs)
	cids := make([]string, nSlugs)
	for i := 0; i < nSlugs; i++ {
		slug := "app" + strconv.Itoa(i)
		cid := "client" + strconv.Itoa(i)
		slugs[i] = slug
		cids[i] = cid
		p.SetPoolMode(slug, config.IsolationPerSession, 0, 1)
		slot := p.reserveWorker(slug, cid)
		if slot < 0 {
			b.Fatalf("reserveWorker failed for %s", slug)
		}
		p.bindClient(slug, cid, slot)
		p.clientConnOpened(slug, cid) // keep liveConns >= 1 so no grace timer churns
	}

	var next int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		idx := int(atomic.AddInt64(&next, 1)-1) % nSlugs
		slug, cid := slugs[idx], cids[idx]
		for pb.Next() {
			p.clientConnOpened(slug, cid)
			p.clientConnClosed(slug, cid)
		}
	})
}
