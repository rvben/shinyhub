package proxy

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxytrust"
)

// clientSlot tracks the live state of one client's binding to an elastic worker.
// It is stored in p.clients[slug][clientID].
//
// slotID is set once at creation (bind) and never mutated, so it is safe to read
// under p.mu.RLock. liveConns and releaseTimer are the per-connection accounting
// state, guarded by cs.mu so a client's open/close bookkeeping runs under a
// per-client lock rather than the global pool write lock - letting unrelated
// clients and unrelated slugs proceed in parallel on the routing hot path.
//
// LOCK ORDERING: cs.mu is ALWAYS acquired while already holding p.mu (read or
// write); it is never held across a p.mu acquisition, which keeps the two-lock
// scheme deadlock-free. Because every cs.mu holder also holds p.mu, a goroutine
// holding the exclusive p.mu WRITE lock is the sole possible accessor and may
// touch liveConns/releaseTimer WITHOUT cs.mu (RWMutex ordering gives it a
// happens-before edge with the RLock paths). cs.mu is therefore only taken on
// the scalable RLock paths, where several readers touch different clients at once.
type clientSlot struct {
	slotID       int
	mu           sync.Mutex
	liveConns    int
	releaseTimer *time.Timer
}

// open records a newly-opened connection for this client: it cancels any pending
// grace timer (a reconnecting client must not have its worker reclaimed) and
// increments liveConns. The caller must hold p.mu (read or write); open takes
// cs.mu internally so it is safe on the shared-lock hot path.
func (cs *clientSlot) open() {
	cs.mu.Lock()
	if cs.releaseTimer != nil {
		cs.releaseTimer.Stop()
		cs.releaseTimer = nil
	}
	cs.liveConns++
	cs.mu.Unlock()
}

// clientGraceTTL is the grace window between the last connection close and
// worker retirement. A var (not const) so tests can shorten it to milliseconds.
var clientGraceTTL = 15 * time.Second

// clientCookiePrefix is the name prefix for the per-slug client-id cookie.
// The full name is clientCookiePrefix + slug.
const clientCookiePrefix = "shinyhub_cid_"

// clientUserTag identifies the authenticated principal a client-id cookie is
// bound to. access.Middleware (which wraps the /app/ handler) puts the user in
// the request context, so an authenticated request tags the cid with its user
// ID; an anonymous request (public app) uses a shared "anon" tag. Binding the
// cid to the user is what stops a shared/kiosk browser from routing a
// subsequently logged-in user to the previous user's dedicated worker: a cid
// signed for one user fails verification for another, who then gets a fresh one.
func clientUserTag(r *http.Request) string {
	if u := auth.UserFromContext(r.Context()); u != nil && u.ID != 0 {
		return strconv.FormatInt(u.ID, 10)
	}
	return "anon"
}

// signClientValue returns the signed client-cookie value "<idhex>.<hmac16>".
// The HMAC-SHA256 binds slug, the user tag, and idhex so a value cannot be
// replayed across apps, across users, or with a modified id.
func signClientValue(key []byte, slug, userTag, idhex string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(slug))
	mac.Write([]byte{0x00})
	mac.Write([]byte(userTag))
	mac.Write([]byte{0x00})
	mac.Write([]byte(idhex))
	return idhex + "." + hex.EncodeToString(mac.Sum(nil))[:16]
}

// verifyClientValue parses and authenticates a signed client-cookie value
// against the given user tag. Returns (idhex, true) when the value has the
// expected format and the HMAC matches; otherwise returns ("", false).
func verifyClientValue(key []byte, slug, userTag, value string) (string, bool) {
	idhex, _, found := strings.Cut(value, ".")
	if !found {
		return "", false
	}
	// Recompute the expected signed form and compare with hmac.Equal to
	// prevent timing-based side-channel attacks.
	expected := signClientValue(key, slug, userTag, idhex)
	if !hmac.Equal([]byte(value), []byte(expected)) {
		return "", false
	}
	return idhex, true
}

// clientID returns the stable client identifier for this request and slug.
// When the request carries a valid signed (or, in unsigned mode, bare) cid
// cookie the stored id is returned with isNew=false. When absent or tampered,
// a fresh random 128-bit id (32 hex chars) is returned with isNew=true.
func (p *Proxy) clientID(r *http.Request, slug string) (id string, isNew bool) {
	cookieName := clientCookiePrefix + slug
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" {
		if key := p.stickySecretBytes(); len(key) > 0 {
			if idhex, ok := verifyClientValue(key, slug, clientUserTag(r), c.Value); ok {
				return idhex, false
			}
			// Tampered or invalid - fall through to generate a new id.
		} else {
			// Unsigned mode: accept a bare 32-hex-char id (no signature).
			if len(c.Value) == 32 {
				return c.Value, false
			}
		}
	}

	// Generate a fresh random 128-bit id.
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is extremely rare; panic is appropriate here
		// because the system's entropy source is broken.
		panic("proxy: crypto/rand failure: " + err.Error())
	}
	return hex.EncodeToString(buf), true
}

// setClientCookie writes the client-id cookie onto w. The cookie value is
// signed when a sticky secret is configured; otherwise the bare id is stored.
// Cookie attributes (Path, HttpOnly, SameSite, Secure) mirror the sticky
// routing cookie exactly: Secure is set when the request was received over
// HTTPS (as determined by X-Forwarded-Proto, trusted only from configured
// proxy CIDRs).
func (p *Proxy) setClientCookie(w http.ResponseWriter, r *http.Request, slug, id string) {
	var value string
	if key := p.stickySecretBytes(); len(key) > 0 {
		value = signClientValue(key, slug, clientUserTag(r), id)
	} else {
		value = id
	}
	http.SetCookie(w, &http.Cookie{
		Name:     clientCookiePrefix + slug,
		Value:    value,
		Path:     "/app/" + slug + "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Mirror the sticky routing cookie's scheme-aware policy: Secure over
		// HTTPS so the cookie is never sent in cleartext. X-Forwarded-Proto is
		// trusted only from configured proxy CIDRs.
		Secure: proxytrust.Scheme(r, p.trustedProxyNets()) == "https",
	})
}

// poolIsElastic reports whether pool is in demand-driven (elastic) mode.
// A pool created by an existing SetPoolSize/SetPoolCap caller has a zero-value
// mode (""), which is intentionally treated as multiplex so the existing
// single-slot behaviour is byte-for-byte unchanged.
func poolIsElastic(pool *backendPool) bool {
	return pool.mode == config.IsolationGrouped || pool.mode == config.IsolationPerSession
}

// workerStates returns a snapshot of every worker in the elastic pool as a
// []workerState that the pure decide() function consumes. Callers must hold
// the pool lock (p.mu) for the duration of the call.
func (pool *backendPool) workerStates() []workerState {
	if len(pool.workers) == 0 {
		return nil
	}
	out := make([]workerState, 0, len(pool.workers))
	for _, w := range pool.workers {
		out = append(out, workerState{
			slotID:          w.slotID,
			assignedClients: w.assignedClients,
			status:          w.status,
		})
	}
	return out
}

// allocateSlotID returns the next monotonically increasing slot ID for this
// pool. IDs are never reused within a pool's lifetime, so a routing pin
// referencing a removed slot is always stale. Callers must hold the pool lock.
func (pool *backendPool) allocateSlotID() int {
	id := pool.nextSlotID
	pool.nextSlotID++
	return id
}

// addElasticWorker inserts a replicaBackend into the elastic workers map.
// r.slotID must already be set (via allocateSlotID). Callers must hold the
// pool lock.
func addElasticWorker(pool *backendPool, r *replicaBackend) {
	if pool.workers == nil {
		pool.workers = make(map[int]*replicaBackend)
	}
	pool.workers[r.slotID] = r
}

// removeElasticWorker removes the worker identified by slotID from the elastic
// workers map. It is a no-op for unknown slot IDs. Callers must hold the pool
// lock.
func removeElasticWorker(pool *backendPool, slotID int) {
	delete(pool.workers, slotID)
}

// reserveWorker atomically allocates a booting slot for a new elastic worker.
// It acquires the write lock, counts active (non-draining) workers, and
// returns -1 when the pool is not elastic or already at maxWorkers. Otherwise
// it inserts a placeholder workerBooting entry and returns its slotID.
//
// The caller MUST NOT hold p.mu when calling this function.
func (p *Proxy) reserveWorker(slug, _ string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	pool, ok := p.pools[slug]
	if !ok || !poolIsElastic(pool) {
		return -1
	}

	// Count active (non-draining) workers: draining slots are leaving and do
	// not consume capacity for new reservations.
	active := 0
	for _, w := range pool.workers {
		if w.status != workerDraining {
			active++
		}
	}
	if active >= pool.maxWorkers {
		return -1
	}

	slotID := pool.allocateSlotID()
	addElasticWorker(pool, &replicaBackend{
		slotID: slotID,
		status: workerBooting,
	})
	return slotID
}

// bindClient records that clientID is assigned to slotID and increments the
// worker's assignedClients counter. Caller must NOT hold p.mu.
func (p *Proxy) bindClient(slug, clientID string, slotID int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bindClientLocked(slug, clientID, slotID)
}

// bindClientLocked records that clientID is assigned to slotID and increments
// the worker's assignedClients counter. A client that already holds a binding
// is migrated, not double-counted: the previous worker's count is decremented
// (it may be draining; a removed worker is simply gone) and any pending
// release timer is stopped so a stale grace expiry cannot fire against the
// new binding. When the migration empties the previous worker, terminate is
// dispatched here - the stopped timer was the only other path that would
// have reclaimed it. Caller MUST hold p.mu WRITE.
func (p *Proxy) bindClientLocked(slug, clientID string, slotID int) {
	pool := p.pools[slug]
	if old := p.lookupClientSlot(slug, clientID); old != nil {
		if old.slotID == slotID {
			return // already bound to this slot
		}
		if old.releaseTimer != nil {
			old.releaseTimer.Stop()
			old.releaseTimer = nil
		}
		if pool != nil {
			if w, ok := pool.workers[old.slotID]; ok {
				w.assignedClients--
				if w.assignedClients == 0 && p.terminate != nil {
					// Dispatch via goroutine: the callback must never run
					// inline under the write lock (re-entry / deadlock).
					go p.terminate(slug, old.slotID)
				}
			}
		}
	}
	if p.clients[slug] == nil {
		p.clients[slug] = make(map[string]*clientSlot)
	}
	p.clients[slug][clientID] = &clientSlot{slotID: slotID}
	if pool != nil {
		if w, ok := pool.workers[slotID]; ok {
			w.assignedClients++
		}
	}
}

// placement is the outcome of placeClient.
type placementKind int

const (
	placedBind           placementKind = iota // bound to slotID; serve the loading page
	placedSaturated                           // every worker at cap and max_workers reached: shed
	placedMemoryPressure                      // a new worker is needed but the memory floor is breached
	placedGone                                // pool vanished or turned non-elastic in the lock gap
)

type placement struct {
	kind         placementKind
	slotID       int   // valid when kind == placedBind
	deploymentID int64 // 0 while the bound worker is still booting
	spawned      bool  // a new slot was reserved; the caller dispatches the spawn callback
}

// placeClient atomically places a fresh elastic client. It re-runs decide()
// against the CURRENT pool state under the write lock - the caller's read-lock
// snapshot goes stale under a cold burst, where every concurrent client sees
// the same empty or under-capacity pool and would otherwise reserve its own
// worker. Serializing placement here is what packs a burst to grouped_size
// clients per booting worker and keeps the effective admission ceiling at
// max_workers x grouped_size instead of collapsing to max_workers.
//
// The outcome is one of: bind to an existing worker (ready or booting), or
// reserve a fresh booting slot and bind to it (spawned=true; the caller
// dispatches the spawn callback outside the lock), or shed. memOK=false
// vetoes only NEW slot reservation - binding adds no process, so it proceeds
// under memory pressure. The caller must NOT hold p.mu.
func (p *Proxy) placeClient(slug, clientID string, memOK bool) placement {
	p.mu.Lock()
	defer p.mu.Unlock()

	pool, ok := p.pools[slug]
	if !ok || !poolIsElastic(pool) {
		return placement{kind: placedGone}
	}

	// A concurrent request for the same client may have bound it in the gap
	// between the caller's RUnlock and this Lock; honor that binding.
	if cs := p.lookupClientSlot(slug, clientID); cs != nil {
		if w := pool.workers[cs.slotID]; w != nil && w.status != workerDraining {
			return placement{kind: placedBind, slotID: cs.slotID, deploymentID: w.deploymentID}
		}
	}

	d := decide(pool.workerStates(), pool.mode, pool.groupedSize, pool.maxWorkers, -1)
	switch d.kind {
	case decisionRoute, decisionBind:
		w := pool.workers[d.slotID]
		p.bindClientLocked(slug, clientID, d.slotID)
		if w.status == workerRunning {
			// The worker became ready in the lock gap. The caller still serves
			// the loading page (it reloads and routes within seconds), so arm
			// the ghost-client grace timer now: RegisterElasticWorker's sweep
			// already ran and will never see this binding.
			p.armClientReleaseLocked(slug, clientID)
		}
		return placement{kind: placedBind, slotID: d.slotID, deploymentID: w.deploymentID}
	case decisionAllocate:
		if !memOK {
			return placement{kind: placedMemoryPressure}
		}
		slotID := pool.allocateSlotID()
		addElasticWorker(pool, &replicaBackend{
			slotID: slotID,
			status: workerBooting,
		})
		p.bindClientLocked(slug, clientID, slotID)
		return placement{kind: placedBind, slotID: slotID, spawned: true}
	default: // decisionReject
		return placement{kind: placedSaturated}
	}
}

// clientConnOpened records that clientID has opened a new connection to its
// assigned worker. It cancels any pending release timer (a reconnecting client
// must not have its worker killed mid-session) and increments liveConns.
// Caller must NOT hold p.mu. Runs under the SHARED read lock plus cs.mu so
// unrelated clients open connections in parallel.
func (p *Proxy) clientConnOpened(slug, clientID string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if cs := p.lookupClientSlot(slug, clientID); cs != nil {
		cs.open()
	}
}

// graceExpiry returns the release-timer callback for one client. It runs under
// the exclusive p.mu WRITE lock, so it reads/writes liveConns and releaseTimer
// without cs.mu (see the clientSlot lock-ordering note). It re-checks that
// liveConns is still zero (a reconnect may have bumped it), removes the client
// slot, decrements the worker's assignedClients, and dispatches p.terminate (via
// goroutine, outside the lock) when the worker reaches zero assigned clients.
//
// armed is the clientSlot this timer was created for. A timer that fires
// after the client has been re-placed (bindClientLocked replaces the slot and
// stops the timer, but a callback already past Stop still runs) finds a
// DIFFERENT clientSlot under the same clientID and must not touch it: without
// the identity check a stale expiry would tear down the fresh binding.
func (p *Proxy) graceExpiry(slug, clientID string, armed *clientSlot) func() {
	return func() {
		p.mu.Lock()
		cs := p.lookupClientSlot(slug, clientID)
		if cs == nil || cs != armed {
			p.mu.Unlock()
			return
		}
		if cs.liveConns != 0 {
			// A reconnect bumped liveConns after the timer was scheduled. Its
			// cs.open() already cleared releaseTimer; clear our stale reference
			// too and leave the slot in place.
			cs.releaseTimer = nil
			p.mu.Unlock()
			return
		}
		slotID := cs.slotID
		cs.releaseTimer = nil

		// Remove the client slot.
		delete(p.clients[slug], clientID)
		if len(p.clients[slug]) == 0 {
			delete(p.clients, slug)
		}

		// Decrement the worker's assignedClients and optionally terminate.
		var term func(string, int)
		if pool, ok := p.pools[slug]; ok {
			if w, ok := pool.workers[slotID]; ok {
				w.assignedClients--
				if w.assignedClients == 0 && p.terminate != nil {
					term = p.terminate
				}
			}
		}
		p.mu.Unlock()
		if term != nil {
			// Dispatch outside the lock to avoid re-entry / deadlock.
			go term(slug, slotID)
		}
	}
}

// armClientReleaseLocked arms the grace-period release timer for a bound client
// that has no live connection (e.g. a ghost client at worker-ready time). It is
// a no-op when the client slot is absent, when liveConns is non-zero, or when a
// timer is already pending. The caller MUST hold p.mu WRITE, under which cs
// fields are touched without cs.mu.
func (p *Proxy) armClientReleaseLocked(slug, clientID string) {
	cs := p.lookupClientSlot(slug, clientID)
	if cs == nil || cs.liveConns != 0 || cs.releaseTimer != nil {
		return
	}
	cs.releaseTimer = time.AfterFunc(clientGraceTTL, p.graceExpiry(slug, clientID, cs))
}

// clientConnClosed records that one connection from clientID has closed. When
// liveConns reaches zero it arms a grace-period timer; if no connection reopens
// within clientGraceTTL the client's slot is deleted, the worker's
// assignedClients is decremented, and - when the worker reaches zero assigned
// clients - p.terminate is dispatched in a goroutine. Caller must NOT hold p.mu.
// Runs under the SHARED read lock plus cs.mu so unrelated clients close
// connections in parallel; the grace timer's callback takes the WRITE lock, so
// it cannot fire while this (or an open) holds the read lock.
func (p *Proxy) clientConnClosed(slug, clientID string) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	cs := p.lookupClientSlot(slug, clientID)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.liveConns <= 0 {
		return // caller misbehavior: more closes than opens; do not go negative or re-arm the timer
	}
	cs.liveConns--
	if cs.liveConns > 0 {
		return
	}
	// liveConns just hit zero: arm the grace-period release timer under cs.mu.
	if cs.releaseTimer == nil {
		cs.releaseTimer = time.AfterFunc(clientGraceTTL, p.graceExpiry(slug, clientID, cs))
	}
}

// ReleaseReservation removes a booting slot that failed to spawn. It also
// cancels and removes any client slots already bound to this slotID (a client
// that pre-bound during the boot window must not be left dangling). Called by
// the spawn callback (Task 12) when a worker fails to start or pass health
// checks. Caller must NOT hold p.mu.
func (p *Proxy) ReleaseReservation(slug string, slotID int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pool, ok := p.pools[slug]
	if !ok {
		return
	}
	removeElasticWorker(pool, slotID)

	// Cancel and remove any client slots already bound to this slotID so they
	// do not reference a nonexistent worker after the boot fails.
	for clientID, cs := range p.clients[slug] {
		if cs.slotID == slotID {
			if cs.releaseTimer != nil {
				cs.releaseTimer.Stop()
				cs.releaseTimer = nil
			}
			delete(p.clients[slug], clientID)
		}
	}
	if len(p.clients[slug]) == 0 {
		delete(p.clients, slug)
	}
}

// DeregisterElasticWorker removes a running elastic worker from the pool and
// cancels any client slots bound to it. It is the clean-up path for a
// successfully-started worker that is being intentionally terminated (see
// ElasticSpawner.Terminate). Idempotent: no-ops on unknown slugs or slotIDs.
// Caller must NOT hold p.mu.
func (p *Proxy) DeregisterElasticWorker(slug string, slotID int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pool, ok := p.pools[slug]
	if !ok {
		return
	}
	removeElasticWorker(pool, slotID)

	for clientID, cs := range p.clients[slug] {
		if cs.slotID == slotID {
			if cs.releaseTimer != nil {
				cs.releaseTimer.Stop()
				cs.releaseTimer = nil
			}
			delete(p.clients[slug], clientID)
		}
	}
	if len(p.clients[slug]) == 0 {
		delete(p.clients, slug)
	}
}

// lookupClientSlot returns the clientSlot for clientID in slug's clients map,
// or nil if absent. Callers must hold p.mu (read or write).
func (p *Proxy) lookupClientSlot(slug, clientID string) *clientSlot {
	if p.clients[slug] == nil {
		return nil
	}
	return p.clients[slug][clientID]
}
