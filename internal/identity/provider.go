package identity

import (
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

// Source is the subset of the DB store the provider needs: the user's IdP
// group snapshot and their per-app membership (owner / member role).
type Source interface {
	GetUserGroups(userID int64) ([]string, error)
	// AppMembershipForUser reports whether the user owns the app and their
	// effective member role ("manager"/"viewer"/"", the highest of the manual
	// membership and any group rule).
	AppMembershipForUser(slug string, userID int64) (isOwner bool, memberRole string, err error)
}

// GroupsSource is kept as the historical name for the groups half of Source.
type GroupsSource interface {
	GetUserGroups(userID int64) ([]string, error)
}

// Payload is everything the proxy injects for one authenticated request.
type Payload struct {
	Username        string
	UserID          string
	Role            string
	AppRole         string
	Email           string
	Name            string
	GroupsHeader    string
	GroupsTruncated bool
	Token           string
}

type cachedGroups struct {
	groups  []string
	expires time.Time
}

// appRoleKey identifies one user's capability on one app. Keyed by the
// immutable app ID (a deleted-and-recreated slug must not inherit cached
// roles), with the slug carried only for the lookup itself.
type appRoleKey struct {
	userID int64
	appID  int64
}

type cachedAppRole struct {
	role    string
	expires time.Time
}

// Provider assembles identity payloads: it resolves the user's IdP groups and
// per-app role through small TTL caches (single-flight per key), sanitizes
// them, and mints the per-app token. One Provider serves the whole process.
type Provider struct {
	secret string
	src    Source

	cacheTTL time.Duration
	cacheMax int

	mu       sync.Mutex
	cache    map[int64]cachedGroups
	inflight map[int64]*sync.WaitGroup

	roleCache    map[appRoleKey]cachedAppRole
	roleInflight map[appRoleKey]*sync.WaitGroup

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

func NewProvider(authSecret string, src Source) *Provider {
	return &Provider{
		secret:            authSecret,
		src:               src,
		cacheTTL:          defaultGroupsCacheTTL,
		cacheMax:          defaultGroupsCacheMax,
		cache:             make(map[int64]cachedGroups),
		inflight:          make(map[int64]*sync.WaitGroup),
		roleCache:         make(map[appRoleKey]cachedAppRole),
		roleInflight:      make(map[appRoleKey]*sync.WaitGroup),
		warnedCommaGroups: make(map[string]struct{}),
	}
}

// PayloadFor builds the identity payload for one request. It never fails:
// a groups lookup error yields an empty group list (the advisory payload
// must not take a request down), and a minting error yields a payload with
// an empty Token (logged).
func (p *Provider) PayloadFor(user *auth.ContextUser, slug string, appID int64) *Payload {
	groups := p.groupsFor(user.ID)
	// warn on the raw list (pre-cap) so omissions past the group cap still surface
	p.warnCommaGroups(groups)
	header, claim, truncated := SanitizeGroups(groups)
	appRole := p.appRoleFor(user, slug, appID)
	tok, err := MintToken(DeriveKey(p.secret, appID), TokenParams{
		UserID: user.ID, Username: user.Username, Role: user.Role, AppRole: appRole,
		Email: user.Email, Name: user.DisplayName,
		Groups: claim, GroupsTruncated: truncated, Slug: slug,
	})
	if err != nil {
		slog.Warn("identity: mint token", "slug", slug, "err", err)
	}
	return &Payload{
		Username:        user.Username,
		UserID:          strconv.FormatInt(user.ID, 10),
		Role:            user.Role,
		AppRole:         appRole,
		Email:           user.Email,
		Name:            user.DisplayName,
		GroupsHeader:    header,
		GroupsTruncated: truncated,
		Token:           tok,
	}
}

// appRole maps ownership, global role, and effective member role onto the
// caller's capability on this app. Everyone who reaches PayloadFor has already
// passed the access gate, so the floor is "viewer".
func appRole(user *auth.ContextUser, isOwner bool, memberRole string) string {
	switch {
	case isOwner:
		return "owner"
	case user.Role == "admin" || user.Role == "operator":
		return "manager"
	case memberRole == "manager":
		return "manager"
	default:
		return "viewer"
	}
}

// appRoleFor resolves the user's app role through the TTL cache with per-key
// single-flight, mirroring groupsFor. A lookup error yields "" (the advisory
// payload must not take a request down); membership changes propagate within
// the cache TTL.
func (p *Provider) appRoleFor(user *auth.ContextUser, slug string, appID int64) string {
	key := appRoleKey{userID: user.ID, appID: appID}
	for {
		p.mu.Lock()
		if c, ok := p.roleCache[key]; ok && time.Now().Before(c.expires) {
			p.mu.Unlock()
			return c.role
		}
		if wg, ok := p.roleInflight[key]; ok {
			p.mu.Unlock()
			wg.Wait()
			continue // re-check the cache the flight just filled
		}
		wg := &sync.WaitGroup{}
		wg.Add(1)
		p.roleInflight[key] = wg
		p.mu.Unlock()

		// Guaranteed cleanup even if the source panics: waiters wake, see a
		// cache miss, and one becomes the next owner (correct retry behavior).
		defer func() {
			p.mu.Lock()
			delete(p.roleInflight, key)
			p.mu.Unlock()
			wg.Done()
		}()

		role := ""
		isOwner, memberRole, err := p.src.AppMembershipForUser(slug, user.ID)
		if err != nil {
			slog.Warn("identity: resolve app role", "slug", slug, "user_id", user.ID, "err", err)
		} else {
			role = appRole(user, isOwner, memberRole)
		}

		p.mu.Lock()
		if len(p.roleCache) >= p.cacheMax {
			p.evictRolesLocked()
		}
		p.roleCache[key] = cachedAppRole{role: role, expires: time.Now().Add(p.cacheTTL)}
		p.mu.Unlock()
		return role
	}
}

// evictRolesLocked drops expired role entries; if none expired, drops an
// arbitrary entry to stay bounded. Caller holds p.mu.
func (p *Provider) evictRolesLocked() {
	now := time.Now()
	for k, c := range p.roleCache {
		if now.After(c.expires) {
			delete(p.roleCache, k)
		}
	}
	if len(p.roleCache) >= p.cacheMax {
		for k := range p.roleCache {
			delete(p.roleCache, k)
			break
		}
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

		// Guaranteed cleanup even if the source panics: waiters wake, see a
		// cache miss, and one becomes the next owner (correct retry behavior).
		defer func() {
			p.mu.Lock()
			delete(p.inflight, userID)
			p.mu.Unlock()
			wg.Done()
		}()

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
		p.mu.Unlock()
		return groups
	}
}

// evictLocked drops expired entries; if none expired, drops an arbitrary
// entry to stay bounded. Caller holds p.mu. The caller always inserts one
// entry immediately after this returns.
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
