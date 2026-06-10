package identity

import (
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

// GroupsSource is the subset of the DB store the provider needs.
type GroupsSource interface {
	GetUserGroups(userID int64) ([]string, error)
}

// Payload is everything the proxy injects for one authenticated request.
type Payload struct {
	Username        string
	UserID          string
	Role            string
	GroupsHeader    string
	GroupsTruncated bool
	Token           string
}

type cachedGroups struct {
	groups  []string
	expires time.Time
}

// Provider assembles identity payloads: it resolves the user's IdP groups
// through a small TTL cache (single-flight per user), sanitizes them, and
// mints the per-app token. One Provider serves the whole process.
type Provider struct {
	secret string
	src    GroupsSource

	cacheTTL time.Duration
	cacheMax int

	mu       sync.Mutex
	cache    map[int64]cachedGroups
	inflight map[int64]*sync.WaitGroup

	// warnedCommaGroups dedups the comma-omission warning per group name,
	// bounded so an IdP feeding unbounded unique names can grow neither
	// memory nor the log (one summary warning on overflow, then suppress).
	warnedCommaGroups map[string]struct{}
	warnedOverflow    bool
}

const (
	defaultGroupsCacheTTL = 30 * time.Second
	defaultGroupsCacheMax = 10_000
	warnedGroupsMax       = 1000
)

func NewProvider(authSecret string, src GroupsSource) *Provider {
	return &Provider{
		secret:            authSecret,
		src:               src,
		cacheTTL:          defaultGroupsCacheTTL,
		cacheMax:          defaultGroupsCacheMax,
		cache:             make(map[int64]cachedGroups),
		inflight:          make(map[int64]*sync.WaitGroup),
		warnedCommaGroups: make(map[string]struct{}),
	}
}

// PayloadFor builds the identity payload for one request. It never fails:
// a groups lookup error yields an empty group list (the advisory payload
// must not take a request down), and a minting error yields a payload with
// an empty Token (logged).
func (p *Provider) PayloadFor(user *auth.ContextUser, slug string, appID int64) *Payload {
	groups := p.groupsFor(user.ID)
	p.warnCommaGroups(groups)
	header, claim, truncated := SanitizeGroups(groups)
	tok, err := MintToken(DeriveKey(p.secret, appID), TokenParams{
		UserID: user.ID, Username: user.Username, Role: user.Role,
		Groups: claim, GroupsTruncated: truncated, Slug: slug,
	})
	if err != nil {
		slog.Warn("identity: mint token", "slug", slug, "err", err)
	}
	return &Payload{
		Username:        user.Username,
		UserID:          strconv.FormatInt(user.ID, 10),
		Role:            user.Role,
		GroupsHeader:    header,
		GroupsTruncated: truncated,
		Token:           tok,
	}
}

// groupsFor returns the user's groups through the TTL cache with per-user
// single-flight so a burst of requests for one user does one DB query.
func (p *Provider) groupsFor(userID int64) []string {
	for {
		p.mu.Lock()
		if c, ok := p.cache[userID]; ok && time.Now().Before(c.expires) {
			p.mu.Unlock()
			return c.groups
		}
		if wg, ok := p.inflight[userID]; ok {
			p.mu.Unlock()
			wg.Wait()
			continue // re-check the cache the flight just filled
		}
		wg := &sync.WaitGroup{}
		wg.Add(1)
		p.inflight[userID] = wg
		p.mu.Unlock()

		groups, err := p.src.GetUserGroups(userID)
		if err != nil {
			slog.Warn("identity: resolve groups", "user_id", userID, "err", err)
			groups = nil
		}

		p.mu.Lock()
		if len(p.cache) >= p.cacheMax {
			p.evictLocked()
		}
		p.cache[userID] = cachedGroups{groups: groups, expires: time.Now().Add(p.cacheTTL)}
		delete(p.inflight, userID)
		p.mu.Unlock()
		wg.Done()
		return groups
	}
}

// evictLocked drops expired entries; if none expired, drops an arbitrary
// entry to stay bounded. Caller holds p.mu.
func (p *Provider) evictLocked() {
	now := time.Now()
	for id, c := range p.cache {
		if now.After(c.expires) {
			delete(p.cache, id)
		}
	}
	if len(p.cache) >= p.cacheMax {
		for id := range p.cache {
			delete(p.cache, id)
			break
		}
	}
}

// warnCommaGroups logs once per comma-bearing group name (bounded set).
func (p *Provider) warnCommaGroups(groups []string) {
	for _, g := range groups {
		if !strings.Contains(g, ",") {
			continue
		}
		p.mu.Lock()
		if _, seen := p.warnedCommaGroups[g]; seen {
			p.mu.Unlock()
			continue
		}
		if len(p.warnedCommaGroups) >= warnedGroupsMax {
			if !p.warnedOverflow {
				p.warnedOverflow = true
				p.mu.Unlock()
				slog.Warn("identity: too many distinct comma-bearing group names; suppressing further warnings")
				continue
			}
			p.mu.Unlock()
			continue
		}
		p.warnedCommaGroups[g] = struct{}{}
		p.mu.Unlock()
		slog.Warn("identity: group name contains a comma; omitted from X-Shinyhub-Groups header (JWT claim carries it)", "group", g)
	}
}
