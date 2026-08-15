package api

import (
	"context"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/process"
)

const (
	providerLogReadTimeout       = 15 * time.Second
	providerLogMaxConcurrentRead = 4
	providerLogShareTTL          = time.Second
	providerLogShareMaxEntries   = 256
)

type providerLogReadKey struct {
	provider  string
	region    string
	logGroup  string
	logStream string
	cursor    string
	limit     int32
}

type providerLogCall struct {
	done chan struct{}
	page process.ExternalLogPage
	err  error
}

type providerLogCacheEntry struct {
	page      process.ExternalLogPage
	expiresAt time.Time
}

// providerLogCoordinator shares one provider request among viewers asking for
// the same immutable stream position. The provider call is detached from any
// one browser request so closing the first tab does not fail the other viewers.
type providerLogCoordinator struct {
	reader   process.ExternalLogReader
	slots    chan struct{}
	mu       sync.Mutex
	inflight map[providerLogReadKey]*providerLogCall
	recent   map[providerLogReadKey]providerLogCacheEntry
}

func newProviderLogCoordinator(reader process.ExternalLogReader) *providerLogCoordinator {
	if reader == nil {
		return nil
	}
	return &providerLogCoordinator{
		reader: reader, slots: make(chan struct{}, providerLogMaxConcurrentRead),
		inflight: make(map[providerLogReadKey]*providerLogCall),
		recent:   make(map[providerLogReadKey]providerLogCacheEntry),
	}
}

func (c *providerLogCoordinator) read(ctx context.Context, details process.ExternalLogs, cursor string, limit int32) (process.ExternalLogPage, bool, error) {
	key := providerLogReadKey{
		provider: details.Provider, region: details.Region,
		logGroup: details.LogGroup, logStream: details.LogStream,
		cursor: cursor, limit: limit,
	}
	c.mu.Lock()
	if cached, ok := c.recent[key]; ok {
		if time.Now().Before(cached.expiresAt) {
			c.mu.Unlock()
			return cached.page, true, nil
		}
		delete(c.recent, key)
	}
	if call := c.inflight[key]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return process.ExternalLogPage{}, true, ctx.Err()
		case <-call.done:
			return call.page, true, call.err
		}
	}
	call := &providerLogCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	go c.execute(ctx, key, details, cursor, limit, call)
	select {
	case <-ctx.Done():
		return process.ExternalLogPage{}, false, ctx.Err()
	case <-call.done:
		return call.page, false, call.err
	}
}

func (c *providerLogCoordinator) execute(parent context.Context, key providerLogReadKey, details process.ExternalLogs, cursor string, limit int32, call *providerLogCall) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), providerLogReadTimeout)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		call.page, call.err = c.reader.Read(ctx, details, cursor, limit)
		<-c.slots
	case <-ctx.Done():
		call.err = ctx.Err()
	}

	c.mu.Lock()
	delete(c.inflight, key)
	if call.err == nil {
		now := time.Now()
		if len(c.recent) >= providerLogShareMaxEntries {
			for cachedKey, cached := range c.recent {
				if !now.Before(cached.expiresAt) {
					delete(c.recent, cachedKey)
				}
			}
		}
		if len(c.recent) >= providerLogShareMaxEntries {
			for cachedKey := range c.recent {
				delete(c.recent, cachedKey)
				break
			}
		}
		c.recent[key] = providerLogCacheEntry{page: call.page, expiresAt: now.Add(providerLogShareTTL)}
	}
	close(call.done)
	c.mu.Unlock()
}
