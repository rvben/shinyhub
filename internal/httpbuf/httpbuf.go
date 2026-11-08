// Package httpbuf recycles the copy buffers httputil.ReverseProxy uses to
// stream a backend response to the client. Without a BufferPool the proxy
// allocates a fresh 32 KiB buffer per response, which was about 90% of the
// bytes the data plane allocated per proxied request.
package httpbuf

import (
	"net/http/httputil"
	"net/url"
	"sync"
)

// Size is the length of every buffer the pool hands out. It matches what
// httputil.ReverseProxy allocates when no pool is set, so streaming behaviour
// is unchanged.
const Size = 32 * 1024

// Pool implements httputil.BufferPool over a sync.Pool. The zero value is
// ready to use.
type Pool struct {
	pool sync.Pool
}

// Get returns a buffer of exactly Size bytes, recycled when one is available.
func (p *Pool) Get() []byte {
	if b, ok := p.pool.Get().(*[]byte); ok {
		return (*b)[:Size]
	}
	return make([]byte, Size)
}

// Put returns b to the pool. A buffer that cannot hold Size bytes is dropped,
// so nothing a caller hands back can shrink what the next Get returns.
func (p *Pool) Put(b []byte) {
	if cap(b) < Size {
		return
	}
	b = b[:Size]
	p.pool.Put(&b)
}

// Buffers is the process-wide pool shared by every reverse proxy.
var Buffers = &Pool{}

// ReverseProxy returns a single-host reverse proxy for target that draws its
// copy buffers from Buffers. Every reverse proxy in the codebase is built
// through this constructor, so none falls back to per-response allocation.
func ReverseProxy(target *url.URL) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.BufferPool = Buffers
	return rp
}
