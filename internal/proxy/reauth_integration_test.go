package proxy_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/proxy"
)

// wsEchoBackend performs a genuine 101 handshake on a hijacked connection and
// then echoes every line for as long as the tunnel lives, like a real Shiny
// worker. The looping echo is what lets a test distinguish "the session is
// still usable" from "the session was closed", across several sweeps.
func wsEchoBackend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			return
		}
		if err := buf.Flush(); err != nil {
			return
		}
		for {
			line, err := buf.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := buf.WriteString("echo:" + line); err != nil {
				return
			}
			if err := buf.Flush(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// liveSession is a WebSocket tunnel opened through the full production chain:
// access.Middleware -> proxy.Proxy -> a WS backend.
type liveSession struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
}

type lifecycleRecorder struct {
	starts chan proxy.UsageSessionStart
	ends   chan string
}

func (r *lifecycleRecorder) StartSession(start proxy.UsageSessionStart) string {
	r.starts <- start
	return "usage-session-1"
}

func (r *lifecycleRecorder) EndSession(id string) { r.ends <- id }

// echo proves the tunnel still carries traffic end to end.
func (s *liveSession) echo(what string) {
	s.t.Helper()
	if err := s.conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		s.t.Fatalf("set deadline: %v", err)
	}
	if _, err := s.conn.Write([]byte(what + "\n")); err != nil {
		s.t.Fatalf("session is not usable: write %q: %v", what, err)
	}
	got, err := s.reader.ReadString('\n')
	if err != nil {
		s.t.Fatalf("session is not usable: read after %q: %v", what, err)
	}
	if got != "echo:"+what+"\n" {
		s.t.Fatalf("echo = %q, want %q", got, "echo:"+what+"\n")
	}
}

// assertClosed requires the tunnel to be dead. The connection is closed at the
// socket without a WebSocket close frame, so the client sees a read error.
func (s *liveSession) assertClosed() {
	s.t.Helper()
	if err := s.conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		s.t.Fatalf("set deadline: %v", err)
	}
	// A write may still succeed into a local buffer; the read is what proves
	// the peer is gone.
	_, _ = s.conn.Write([]byte("still-there\n"))
	if _, err := s.reader.ReadString('\n'); err == nil {
		s.t.Fatal("the revoked session is still carrying traffic; it must have been closed")
	}
}

// reauthChain is the production wiring under test, assembled from real parts: a
// real database, the real access middleware, the real proxy, and a real WS
// backend.
type reauthChain struct {
	store *db.Store
	proxy *proxy.Proxy
	front *httptest.Server
	ana   *db.User
}

func newReauthChain(t *testing.T, appAccess string) *reauthChain {
	t.Helper()
	const (
		slug   = "rep"
		secret = "recheck-chain-secret"
	)

	store := dbtest.New(t)
	for _, u := range []struct{ name, role string }{{"owner", "admin"}, {"ana", "developer"}} {
		if err := store.CreateUser(db.CreateUserParams{Username: u.name, PasswordHash: "h", Role: u.role}); err != nil {
			t.Fatalf("create user %s: %v", u.name, err)
		}
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}
	ana, err := store.GetUserByUsername("ana")
	if err != nil {
		t.Fatalf("get ana: %v", err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: "Report", OwnerID: owner.ID}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := store.SetAppAccess(slug, appAccess); err != nil {
		t.Fatalf("set access: %v", err)
	}
	if err := store.GrantAppAccess(slug, ana.ID); err != nil {
		t.Fatalf("grant access: %v", err)
	}

	backend := wsEchoBackend(t)
	prx := proxy.New()
	if err := prx.Register(slug, backend.URL); err != nil {
		t.Fatalf("Register: %v", err)
	}

	mw := access.Middleware(store, secret, store.IsTokenRevoked, store.LookupContextUser)
	front := httptest.NewServer(mw(prx))
	t.Cleanup(front.Close)

	return &reauthChain{store: store, proxy: prx, front: front, ana: ana}
}

const reauthSecret = "recheck-chain-secret"

// sweep runs exactly one pass of the production re-authorization, adapting
// proxy.ConnPrincipal to access.Principal the same way cmd/shinyhub does. Going
// through the real adapter is deliberate: a field silently dropped in that
// mapping would make every sweep decide on a zero value, and no unit test on
// either side of the boundary would notice.
func (c *reauthChain) sweep() int {
	return c.proxy.RecheckSessions(func(p proxy.ConnPrincipal) (bool, string, error) {
		return access.Recheck(c.store, c.store.LookupContextUser, c.store.IsTokenRevoked, access.Principal{
			Slug:         p.Slug,
			UserID:       p.UserID,
			Role:         p.Role,
			SessionEpoch: p.SessionEpoch,
			JTI:          p.JTI,
		})
	})
}

// openSession signs in as ana and completes a real 101 upgrade through the
// chain. Passing an empty cookie opens the session anonymously.
func (c *reauthChain) openSession(t *testing.T, cookie string) *liveSession {
	t.Helper()
	conn, err := net.DialTimeout("tcp", c.front.Listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	req := "GET /app/rep/websocket HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"
	if cookie != "" {
		req += "Cookie: " + auth.SessionCookieName + "=" + cookie + "\r\n"
	}
	req += "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if statusLine != "HTTP/1.1 101 Switching Protocols\r\n" {
		t.Fatalf("status line = %q, want 101 Switching Protocols", statusLine)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	s := &liveSession{t: t, conn: conn, reader: reader}
	s.echo("hello")
	if n := c.proxy.ActiveUpgradedConns(); n != 1 {
		t.Fatalf("tracked upgraded conns = %d, want 1 (the upgrade was not registered)", n)
	}
	return s
}

// sessionCookie mints the session token a fresh login would hand out, carrying
// the user's live epoch exactly as IssueSessionToken does in production.
func (c *reauthChain) sessionCookie(t *testing.T) string {
	t.Helper()
	u, err := c.store.LookupContextUser(c.ana.ID)
	if err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	tok, err := auth.IssueSessionToken(u, reauthSecret)
	if err != nil {
		t.Fatalf("IssueSessionToken: %v", err)
	}
	return tok
}

func TestUsageLifecycleMatchesSuccessfulWebSocketConnection(t *testing.T) {
	c := newReauthChain(t, "private")
	recorder := &lifecycleRecorder{
		starts: make(chan proxy.UsageSessionStart, 1),
		ends:   make(chan string, 1),
	}
	c.proxy.SetUsageRecorder(recorder)
	session := c.openSession(t, c.sessionCookie(t))

	select {
	case start := <-recorder.starts:
		if start.Slug != "rep" || start.UserID != c.ana.ID || start.StartedAt.IsZero() {
			t.Fatalf("usage start = %+v", start)
		}
	case <-time.After(time.Second):
		t.Fatal("successful WebSocket upgrade did not start usage session")
	}

	if err := session.conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-recorder.ends:
		if id != "usage-session-1" {
			t.Fatalf("ended id = %q", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing WebSocket did not end usage session")
	}
}

// This is the reported defect, end to end. An HTTP request passes through the
// access middleware once; a WebSocket upgrade is one such request, and the
// connection it produces used to outlive every later authorization decision.
// Revoking the user's sessions left the session they already had open running
// for hours.
func TestReauthChain_RevokingSessionsClosesTheLiveWebSocket(t *testing.T) {
	c := newReauthChain(t, "private")
	s := c.openSession(t, c.sessionCookie(t))

	// Negative control: the sweep is running and must leave a valid session
	// alone, or "the connection closed" would prove nothing below.
	if closed := c.sweep(); closed != 0 {
		t.Fatalf("sweep closed %d connections before any revocation, want 0", closed)
	}
	s.echo("still-authorized")

	if err := c.store.BumpTokenEpoch(c.ana.ID); err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}

	if closed := c.sweep(); closed != 1 {
		t.Fatalf("sweep closed %d connections after revoking sessions, want 1", closed)
	}
	s.assertClosed()
	if n := c.proxy.ActiveUpgradedConns(); n != 0 {
		t.Errorf("tracked upgraded conns after the sweep = %d, want 0", n)
	}

	// The client reconnects and now meets the middleware, which denies it. This
	// is why the connection is closed at the socket rather than with a close
	// frame: the proper 401 comes from the request path.
	resp, err := c.front.Client().Get(c.front.URL + "/app/rep/")
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reconnect status = %d, want 401", resp.StatusCode)
	}
}

// A user whose sessions were revoked in the past logs in again. Their epoch is
// no longer zero, and the sweep must leave the new session alone.
//
// This is the failure mode that would be catastrophic rather than merely
// incomplete: a principal that captures no epoch (or a zero one) compares
// unequal to every previously-revoked user's live epoch, so the first sweep
// would close every session those users have. The negative controls elsewhere
// all run at epoch zero and would not notice.
func TestReauthChain_PreviouslyRevokedUserKeepsANewSession(t *testing.T) {
	c := newReauthChain(t, "private")
	for i := 0; i < 2; i++ {
		if err := c.store.BumpTokenEpoch(c.ana.ID); err != nil {
			t.Fatalf("revoke sessions: %v", err)
		}
	}
	u, err := c.store.LookupContextUser(c.ana.ID)
	if err != nil {
		t.Fatalf("lookup user: %v", err)
	}
	if u.TokenEpoch == 0 {
		t.Fatal("precondition: the user's epoch must be non-zero for this test to mean anything")
	}

	s := c.openSession(t, c.sessionCookie(t))
	if closed := c.sweep(); closed != 0 {
		t.Fatalf("sweep closed %d valid sessions of a previously-revoked user, want 0", closed)
	}
	s.echo("still-authorized")

	// And the sweep still closes it once the sessions are revoked again.
	if err := c.store.BumpTokenEpoch(c.ana.ID); err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}
	if closed := c.sweep(); closed != 1 {
		t.Fatalf("sweep closed %d connections after revoking again, want 1", closed)
	}
	s.assertClosed()
}

// Removing a user from an app closes the session they have open on it.
func TestReauthChain_RemovingAppAccessClosesTheLiveWebSocket(t *testing.T) {
	c := newReauthChain(t, "private")
	s := c.openSession(t, c.sessionCookie(t))

	if err := c.store.RevokeAppAccess("rep", c.ana.ID); err != nil {
		t.Fatalf("revoke app access: %v", err)
	}
	if closed := c.sweep(); closed != 1 {
		t.Fatalf("sweep closed %d connections after removing access, want 1", closed)
	}
	s.assertClosed()
}

// A public app admits anonymous connections. Flipping it to private strands
// them, and the sweep is the only thing that can find them again.
func TestReauthChain_PublicToPrivateClosesAnonymousWebSocket(t *testing.T) {
	c := newReauthChain(t, "public")
	s := c.openSession(t, "")

	if closed := c.sweep(); closed != 0 {
		t.Fatalf("sweep closed %d anonymous connections on a public app, want 0", closed)
	}
	s.echo("still-public")

	if err := c.store.SetAppAccess("rep", "private"); err != nil {
		t.Fatalf("set private: %v", err)
	}
	if closed := c.sweep(); closed != 1 {
		t.Fatalf("sweep closed %d connections after the app went private, want 1", closed)
	}
	s.assertClosed()
}

// The sweep runs on a timer in production. This drives that path rather than
// calling RecheckSessions directly, so a broken ticker or a mis-wired stop
// cannot pass unnoticed.
func TestReauthChain_TimerDrivenSweepClosesRevokedSession(t *testing.T) {
	c := newReauthChain(t, "private")
	s := c.openSession(t, c.sessionCookie(t))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.proxy.StartSessionRecheck(ctx, 10*time.Millisecond, func(p proxy.ConnPrincipal) (bool, string, error) {
			return access.Recheck(c.store, c.store.LookupContextUser, c.store.IsTokenRevoked, access.Principal{
				Slug:         p.Slug,
				UserID:       p.UserID,
				Role:         p.Role,
				SessionEpoch: p.SessionEpoch,
				JTI:          p.JTI,
			})
		})
	}()
	defer func() {
		cancel()
		<-done
	}()

	// Several sweeps pass with the session still valid.
	time.Sleep(50 * time.Millisecond)
	s.echo("still-authorized")

	if err := c.store.BumpTokenEpoch(c.ana.ID); err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for c.proxy.ActiveUpgradedConns() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := c.proxy.ActiveUpgradedConns(); n != 0 {
		t.Fatalf("the timer-driven sweep left %d revoked connections open", n)
	}
	s.assertClosed()
}
