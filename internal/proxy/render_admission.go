package proxy

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/admission"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/proxytrust"
)

// renderPrincipal returns the fairness key for this request under slug's access
// mode. The key is chosen by the app's access mode, not by what the request
// presents, so a client cannot pick which bucket it lands in: a public app keys
// on the trusted client IP even when a user is present, and a private or shared
// app keys on the authenticated user id (access middleware has already rejected
// anonymous requests for those modes). When the access lookup is unwired or
// returns an unknown mode, both keys are combined so neither gate is skipped,
// which is conservative in both directions.
func (p *Proxy) renderPrincipal(r *http.Request, slug string) string {
	mode := ""
	if fp := p.appAccessLookup.Load(); fp != nil {
		mode = (*fp)(slug)
	}
	ip := proxytrust.ClientIP(r, p.trustedProxyNets())
	uid := int64(0)
	if u := auth.UserFromContext(r.Context()); u != nil {
		uid = u.ID
	}
	switch mode {
	case "public":
		return "ip:" + ip
	case "private", "shared":
		return "u:" + strconv.FormatInt(uid, 10)
	default:
		return "ip:" + ip + "|u:" + strconv.FormatInt(uid, 10)
	}
}

// parkBudget bounds how many render-paced WebSocket upgrades may be parked at
// once, per app and host-wide. A parked upgrade holds a goroutine and a socket,
// so an unbounded park queue under a stampede is itself a resource exhaustion;
// the ceilings cap it. A non-positive ceiling means that dimension is unlimited.
type parkBudget struct {
	mu     sync.Mutex
	perApp int
	total  int
	byApp  map[string]int
	inUse  int
}

func newParkBudget(perApp, total int) *parkBudget {
	return &parkBudget{perApp: perApp, total: total, byApp: make(map[string]int)}
}

// acquire takes a park slot for slug if both the per-app and global ceilings
// allow it, returning whether it did. A refused acquire takes nothing.
func (b *parkBudget) acquire(slug string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total > 0 && b.inUse >= b.total {
		return false
	}
	if b.perApp > 0 && b.byApp[slug] >= b.perApp {
		return false
	}
	b.inUse++
	b.byApp[slug]++
	return true
}

// release returns a park slot for slug. It is a no-op guard against underflow.
func (b *parkBudget) release(slug string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inUse > 0 {
		b.inUse--
	}
	if b.byApp[slug] > 0 {
		b.byApp[slug]--
		if b.byApp[slug] == 0 {
			delete(b.byApp, slug)
		}
	}
}

// renderParkTTL bounds how long a render-paced upgrade parks before it is shed.
// It must stay well below the client heartbeat timeout: parking past it would
// manufacture the disconnect this feature prevents. renderParkInterval is the
// re-check cadence. Vars so tests can shorten them.
var (
	renderParkTTL      = 2 * time.Second
	renderParkInterval = 100 * time.Millisecond
)

// SetRenderParkTTL sets how long a render-paced upgrade parks before it is
// shed. Safe to call at startup before serving.
func (p *Proxy) SetRenderParkTTL(d time.Duration) { renderParkTTL = d }

// chargeRenderAdmission charges one token for a WebSocket upgrade about to be
// forwarded to slug's backend. It returns true when the upgrade may proceed
// (not a WS upgrade, pacing disabled, or a token taken possibly after a brief
// park) and false when it has been shed, in which case it has already written
// the 503 and recorded the reject. It must be called AFTER p.mu is released and
// AFTER the connection-accounting defers are installed, so a shed unwinds
// through the same cleanup a closed connection uses.
func (p *Proxy) chargeRenderAdmission(rec http.ResponseWriter, r *http.Request, slug string) bool {
	if !isWSUpgrade(r) {
		return true // only a real render (the WS session) is charged
	}
	lim := p.appLimiter(slug)
	if lim == nil {
		return true // pacing disabled for this app
	}
	principal := p.renderPrincipal(r, slug)

	// attempt runs the correct admission stage for this request's progress and
	// reports one of three outcomes. The host CPU watermark is checked FIRST on
	// every attempt, before the limiter is touched: the watermark's Admit is
	// read-only while the limiter consumes a token, so a saturated watermark must
	// never reach the limiter or it would spend a token for a session it blocks.
	//
	// The per-principal fairness stage is charged AT MOST ONCE per request. Once
	// the principal has passed its own gate but the shared app bucket was empty
	// (SharedExhausted), later attempts re-check the SHARED bucket only, via
	// TrySharedOnly. Re-running the full two-stage Admit on every park tick would
	// re-debit the principal's small bucket for capacity it never received, and
	// with a low per-principal refill rate that would lock a legitimate principal
	// out for minutes after shared capacity recovered.
	const (
		attemptWait    = 0
		attemptForward = 1
		attemptShed    = -1
	)
	charged := false
	attempt := func() int {
		if !p.watermarkAdmits() {
			return attemptWait // watermark blocks; wait, no token spent
		}
		if charged {
			if lim.TrySharedOnly() {
				return attemptForward
			}
			return attemptWait
		}
		switch lim.Admit(principal) {
		case admission.Admitted:
			return attemptForward
		case admission.PrincipalExhausted:
			// Over its own fair share. Parking cannot refill the principal bucket
			// within the TTL, so shed now rather than hold a slot uselessly.
			return attemptShed
		default: // admission.SharedExhausted: principal charged, app at capacity
			charged = true
			return attemptWait
		}
	}

	// Fast path: admit or shed without parking when the outcome is already known.
	switch attempt() {
	case attemptForward:
		return true
	case attemptShed:
		return p.shedRender(rec, slug, ReasonRenderPaced)
	}

	// Park: hold briefly under the budget, re-trying, up to the TTL. The budget
	// bounds concurrent parks; when it is full, shed immediately rather than
	// queue a park that costs a socket.
	budget := p.renderPark.Load()
	if budget != nil {
		if !budget.acquire(slug) {
			return p.shedRender(rec, slug, ReasonRenderPaced)
		}
		defer budget.release(slug)
	}
	deadline := time.Now().Add(renderParkTTL)
	ticker := time.NewTicker(renderParkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return p.shedRender(rec, slug, ReasonRenderPaced)
		case <-ticker.C:
			switch attempt() {
			case attemptForward:
				return true
			case attemptShed:
				return p.shedRender(rec, slug, ReasonRenderPaced)
			}
			if time.Now().After(deadline) {
				// Distinguish the two shed reasons: if the watermark is the
				// blocker, report cpu-saturation, else render-paced.
				if !p.watermarkAdmits() {
					return p.shedRender(rec, slug, ReasonCPUSaturation)
				}
				return p.shedRender(rec, slug, ReasonRenderPaced)
			}
		}
	}
}

// watermarkAdmits reports whether the host CPU watermark permits a new session.
// A nil watermark fails open (admits).
func (p *Proxy) watermarkAdmits() bool {
	wm := p.cpuWatermark.Load()
	return wm == nil || wm.Admit()
}

// shedRender writes the 503 render-admission rejection and returns false so the
// caller does not forward. It records the reject for the access log and metrics.
// Retry-After advises the park TTL, rounded up to whole seconds with a floor of
// 1, so the hint tracks the configured pacing window instead of a magic constant.
func (p *Proxy) shedRender(rec http.ResponseWriter, slug string, reason RejectReason) bool {
	p.recordReject(rec, slug, reason, true)
	retry := int((renderParkTTL + time.Second - 1) / time.Second)
	if retry < 1 {
		retry = 1
	}
	rec.Header().Set("Retry-After", strconv.Itoa(retry))
	http.Error(rec, MsgPoolSaturated, http.StatusServiceUnavailable)
	return false
}
