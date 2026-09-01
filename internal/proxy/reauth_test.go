package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

// keepAll is the reauthorizer that never revokes anything.
func keepAll(ConnPrincipal) (bool, string, error) { return false, "", nil }

// countingReauthorizer records which principals it was asked about, so a test
// can assert the sweep decides once per principal rather than once per
// connection.
type countingReauthorizer struct {
	mu    sync.Mutex
	calls []ConnPrincipal
	fn    Reauthorizer
}

func (c *countingReauthorizer) recheck(p ConnPrincipal) (bool, string, error) {
	c.mu.Lock()
	c.calls = append(c.calls, p)
	c.mu.Unlock()
	if c.fn == nil {
		return false, "", nil
	}
	return c.fn(p)
}

func (c *countingReauthorizer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func TestRecheckSessions_ClosesOnlyRevokedPrincipals(t *testing.T) {
	p := New()
	revokedUser := ConnPrincipal{Slug: "rep", UserID: 1, Role: "developer"}
	keptUser := ConnPrincipal{Slug: "rep", UserID: 2, Role: "developer"}
	gone, stays := &stubConn{}, &stubConn{}
	p.conns.track(gone, revokedUser)
	p.conns.track(stays, keptUser)

	closed := p.RecheckSessions(func(c ConnPrincipal) (bool, string, error) {
		return c.UserID == revokedUser.UserID, "sessions revoked", nil
	})

	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if !gone.isClosed() {
		t.Error("the revoked user's connection must be closed")
	}
	if stays.isClosed() {
		t.Error("an unaffected user's connection must stay open")
	}
	if n := p.ActiveUpgradedConns(); n != 1 {
		t.Errorf("tracked conns after the sweep = %d, want 1 (a closed conn must unregister)", n)
	}
}

// A user with eight tabs on one app is one decision, not eight database
// round-trips per sweep.
func TestRecheckSessions_DecidesOncePerPrincipal(t *testing.T) {
	p := New()
	ana := ConnPrincipal{Slug: "rep", UserID: 1, Role: "developer", JTI: "j1"}
	bob := ConnPrincipal{Slug: "rep", UserID: 2, Role: "developer", JTI: "j2"}
	anaConns := []*stubConn{{}, {}, {}}
	for _, c := range anaConns {
		p.conns.track(c, ana)
	}
	p.conns.track(&stubConn{}, bob)

	counter := &countingReauthorizer{fn: func(c ConnPrincipal) (bool, string, error) {
		return c.UserID == ana.UserID, "sessions revoked", nil
	}}
	closed := p.RecheckSessions(counter.recheck)

	if closed != len(anaConns) {
		t.Fatalf("closed = %d, want %d (every tab of the revoked user)", closed, len(anaConns))
	}
	if got := counter.count(); got != 2 {
		t.Errorf("reauthorizer called %d times, want 2 (one per distinct principal)", got)
	}
	for i, c := range anaConns {
		if !c.isClosed() {
			t.Errorf("tab %d of the revoked user is still open", i)
		}
	}
}

// The same identity on two different apps is two principals: losing access to
// one app must not close the session on the other.
func TestRecheckSessions_PrincipalIsPerApp(t *testing.T) {
	p := New()
	onRep := ConnPrincipal{Slug: "rep", UserID: 1, Role: "developer"}
	onDash := ConnPrincipal{Slug: "dash", UserID: 1, Role: "developer"}
	repConn, dashConn := &stubConn{}, &stubConn{}
	p.conns.track(repConn, onRep)
	p.conns.track(dashConn, onDash)

	closed := p.RecheckSessions(func(c ConnPrincipal) (bool, string, error) {
		return c.Slug == "rep", "access to the app was removed", nil
	})

	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if !repConn.isClosed() {
		t.Error("the session on the app that revoked access must close")
	}
	if dashConn.isClosed() {
		t.Error("the same user's session on another app must stay open")
	}
}

// A database blip must not drop every live session on the instance, so an
// undecidable verdict keeps the connection.
func TestRecheckSessions_ErrorKeepsConnections(t *testing.T) {
	p := New()
	c := &stubConn{}
	p.conns.track(c, ConnPrincipal{Slug: "rep", UserID: 1})

	closed := p.RecheckSessions(func(ConnPrincipal) (bool, string, error) {
		return true, "sessions revoked", errors.New("database unavailable")
	})

	if closed != 0 {
		t.Fatalf("closed = %d, want 0 (an error must fail open)", closed)
	}
	if c.isClosed() {
		t.Error("a connection must survive a failed recheck")
	}
}

// A revoked verdict with no reason still closes the connection; the log line
// falls back to a generic reason rather than dropping the close.
func TestRecheckSessions_RevokedWithoutReasonStillCloses(t *testing.T) {
	p := New()
	c := &stubConn{}
	p.conns.track(c, ConnPrincipal{Slug: "rep", UserID: 1})

	if closed := p.RecheckSessions(func(ConnPrincipal) (bool, string, error) {
		return true, "", nil
	}); closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if !c.isClosed() {
		t.Error("a revoked connection must close even without a stated reason")
	}
}

func TestRecheckSessions_NilReauthorizerKeepsEverything(t *testing.T) {
	p := New()
	c := &stubConn{}
	p.conns.track(c, ConnPrincipal{Slug: "rep", UserID: 1})

	if closed := p.RecheckSessions(nil); closed != 0 {
		t.Fatalf("closed = %d, want 0", closed)
	}
	if c.isClosed() {
		t.Error("a nil reauthorizer must not close anything")
	}
}

func TestRecheckSessions_NoConnectionsDoesNoWork(t *testing.T) {
	p := New()
	counter := &countingReauthorizer{}
	if closed := p.RecheckSessions(counter.recheck); closed != 0 {
		t.Fatalf("closed = %d, want 0", closed)
	}
	if got := counter.count(); got != 0 {
		t.Errorf("reauthorizer called %d times with no live connections, want 0", got)
	}
}

func TestStartSessionRecheck_DisabledReturnsImmediately(t *testing.T) {
	cases := []struct {
		name     string
		interval time.Duration
		fn       Reauthorizer
	}{
		{"zero interval", 0, keepAll},
		{"negative interval", -time.Second, keepAll},
		{"nil reauthorizer", time.Millisecond, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := New()
			done := make(chan struct{})
			go func() {
				// A context that is never cancelled: only the disabled guard can
				// make this return.
				p.StartSessionRecheck(context.Background(), tc.interval, tc.fn)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("StartSessionRecheck must return immediately when disabled")
			}
		})
	}
}

func TestStartSessionRecheck_SweepsUntilContextIsDone(t *testing.T) {
	p := New()
	p.conns.track(&stubConn{}, ConnPrincipal{Slug: "rep", UserID: 1})
	counter := &countingReauthorizer{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.StartSessionRecheck(ctx, 5*time.Millisecond, counter.recheck)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for counter.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if counter.count() == 0 {
		t.Fatal("StartSessionRecheck never swept")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartSessionRecheck must return once the context is cancelled")
	}

	// It must be genuinely stopped, not merely slow to log.
	after := counter.count()
	time.Sleep(30 * time.Millisecond)
	if got := counter.count(); got != after {
		t.Errorf("swept %d more times after the context was cancelled", got-after)
	}
}

func TestConnPrincipal_CapturesTheAdmittedIdentity(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/app/rep/websocket", nil)
	ctx := auth.WithUser(r.Context(), &auth.ContextUser{ID: 7, Username: "ana", Role: "developer", TokenEpoch: 3})
	ctx = auth.WithTokenInfo(ctx, &auth.TokenInfo{JTI: "session-jti"})
	r = r.WithContext(ctx)

	got := connPrincipal(r, "rep", 42)
	want := ConnPrincipal{Slug: "rep", RoutedAppID: 42, UserID: 7, Role: "developer", SessionEpoch: 3, JTI: "session-jti"}
	if got != want {
		t.Errorf("connPrincipal = %+v, want %+v", got, want)
	}
}

func TestConnPrincipal_CapturesSupportActorAndDeadline(t *testing.T) {
	expires := time.Now().Add(10 * time.Minute).Round(time.Second)
	r := httptest.NewRequest(http.MethodGet, "/app/rep/websocket", nil)
	user := &auth.ContextUser{ID: 7, Username: "ana", Role: "developer", TokenEpoch: 3,
		SupportSession: &auth.SupportSessionContext{
			ID: "support-id", ActorID: 2, ActorUsername: "admin", ActorTokenEpoch: 5,
			AppID: 42, AppSlug: "rep", ExpiresAt: expires,
		}}
	r = r.WithContext(auth.WithUser(r.Context(), user))
	got := connPrincipal(r, "rep", 42)
	want := ConnPrincipal{Slug: "rep", UserID: 7, Role: "developer", SessionEpoch: 3,
		ActorID: 2, ActorRole: "admin", ActorSessionEpoch: 5, SupportAppID: 42, RoutedAppID: 42, SupportExpiresAt: expires}
	if got != want {
		t.Errorf("connPrincipal = %+v, want %+v", got, want)
	}
}

// An anonymous request on a public app has no identity to revoke, only the
// app's openness to re-check.
func TestConnPrincipal_AnonymousRequestCarriesOnlyTheSlug(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/app/rep/websocket", nil)
	got := connPrincipal(r, "rep", 42)
	if want := (ConnPrincipal{Slug: "rep", RoutedAppID: 42}); got != want {
		t.Errorf("connPrincipal = %+v, want %+v", got, want)
	}
}

// A forward-auth identity is resolved upstream and carries no session token of
// ShinyHub's, so there is no jti to revoke - but the user, role and epoch are
// still the live ones and must be swept.
func TestConnPrincipal_ForwardAuthIdentityHasNoJTI(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/app/rep/websocket", nil)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{ID: 9, Username: "fa", Role: "admin", TokenEpoch: 2}))

	got := connPrincipal(r, "rep", 42)
	want := ConnPrincipal{Slug: "rep", RoutedAppID: 42, UserID: 9, Role: "admin", SessionEpoch: 2}
	if got != want {
		t.Errorf("connPrincipal = %+v, want %+v", got, want)
	}
}

// The sweep and the shutdown drain both walk the tracker and close what they
// find, on different goroutines, over the same connections. Each takes a
// snapshot under the lock and closes outside it, and each Close unregisters
// through that same lock - the arrangement that must not deadlock and must not
// race. Runs both against a shared set while new connections keep arriving, so
// the tracker is mutated underneath both.
func TestRecheckSessions_RacesTheDrainSafely(t *testing.T) {
	p := New()
	principal := ConnPrincipal{Slug: "rep", UserID: 1, Role: "developer", SessionEpoch: 1}
	conns := make([]*stubConn, 200)
	for i := range conns {
		conns[i] = &stubConn{}
		p.conns.track(conns[i], principal)
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 20 {
			p.RecheckSessions(func(ConnPrincipal) (bool, string, error) {
				return true, "sessions revoked", nil
			})
		}
	}()
	go func() {
		defer wg.Done()
		for range 20 {
			p.conns.closeAll()
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			p.conns.track(&stubConn{}, principal)
		}
	}()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("sweep and drain deadlocked over the connection tracker")
	}

	for i, c := range conns {
		if !c.isClosed() {
			t.Fatalf("connection %d survived both a revocation sweep and a full drain", i)
		}
	}
	// Every connection either was closed by one of the two, or arrived after
	// the last pass; nothing may be left half-registered.
	p.conns.closeAll()
	if n := p.conns.count(); n != 0 {
		t.Fatalf("tracker still holds %d connections after a final drain", n)
	}
}
