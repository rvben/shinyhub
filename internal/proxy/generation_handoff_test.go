package proxy_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestGenerationHandoffPreservesOpenRequestAndRepinsNewRequests(t *testing.T) {
	t.Parallel()

	v1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "v1")
	}))
	defer v1.Close()
	v2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "v2")
	}))
	defer v2.Close()

	p := proxy.New()
	p.SetPoolSize("dashboard", 1)
	if err := p.RegisterReplica("dashboard", 0, v1.URL, nil, 101); err != nil {
		t.Fatalf("register v1: %v", err)
	}

	oldCookie := generationRequest(t, p, nil, "v1")

	if err := p.StageGeneration("dashboard", 202, 1); err != nil {
		t.Fatalf("stage v2: %v", err)
	}
	if err := p.RegisterGenerationReplica("dashboard", 202, 0, v2.URL, nil); err != nil {
		t.Fatalf("register v2 candidate: %v", err)
	}

	// Merely-ready candidates are unpublished. A browser without affinity must
	// still receive the active generation until the explicit cutover.
	generationRequest(t, p, nil, "v1")

	previous, err := p.ActivateGeneration("dashboard", 202)
	if err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	if previous != 101 {
		t.Fatalf("previous generation = %d, want 101", previous)
	}

	// A post-cutover request does not re-enter the draining generation merely
	// because it carries an old cookie. Existing open tunnels keep their already
	// selected backend (covered below); all new work enters v2.
	repinned := generationRequest(t, p, oldCookie, "v2")
	if repinned == nil || repinned.Value == oldCookie.Value {
		t.Fatal("old generation cookie was not refreshed at cutover")
	}
	generationRequest(t, p, nil, "v2")

	if !p.TryRetireGeneration("dashboard", 101) {
		t.Fatal("idle retirement of v1 returned false")
	}

	// A cookie for a retired generation is stale and is replaced without a
	// loading/deploying interstitial.
	repinned = generationRequest(t, p, oldCookie, "v2")
	if repinned == nil || repinned.Value == oldCookie.Value {
		t.Fatal("retired generation cookie was not refreshed")
	}
}

func TestGenerationHandoffKeepsOpenWebSocketWhileNewWorkUsesActive(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("grouped=%t", grouped), func(t *testing.T) {
			backend := func(version string) *httptest.Server {
				return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
						_, _ = io.WriteString(w, version)
						return
					}
					conn, buf, err := w.(http.Hijacker).Hijack()
					if err != nil {
						return
					}
					defer conn.Close()
					_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
					_ = buf.Flush()
					for {
						line, err := buf.ReadString('\n')
						if err != nil {
							return
						}
						_, _ = buf.WriteString(version + ":" + line)
						_ = buf.Flush()
					}
				}))
			}
			v1, v2 := backend("v1"), backend("v2")
			defer v1.Close()
			defer v2.Close()
			p := proxy.New()
			p.SetPoolSize("dashboard", 1)
			if grouped {
				p.SetPoolMode("dashboard", config.IsolationGrouped, 4, 2)
				if err := p.RegisterElasticWorker("dashboard", 0, v1.URL, nil, 101); err != nil {
					t.Fatal(err)
				}
			} else if err := p.RegisterReplica("dashboard", 0, v1.URL, nil, 101); err != nil {
				t.Fatal(err)
			}
			oldCookies := generationRequestCookies(t, p, nil, "v1")
			front := httptest.NewServer(p)
			defer front.Close()

			openWS := func(cookies []*http.Cookie) (net.Conn, *bufio.Reader) {
				conn, err := net.DialTimeout("tcp", front.Listener.Addr().String(), 2*time.Second)
				if err != nil {
					t.Fatalf("dial proxy: %v", err)
				}
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				header := ""
				if len(cookies) > 0 {
					values := make([]string, 0, len(cookies))
					for _, cookie := range cookies {
						values = append(values, cookie.Name+"="+cookie.Value)
					}
					header = "Cookie: " + strings.Join(values, "; ") + "\r\n"
				}
				_, _ = io.WriteString(conn, "GET /app/dashboard/ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+header+"\r\n")
				reader := bufio.NewReader(conn)
				status, err := reader.ReadString('\n')
				if err != nil || !strings.Contains(status, "101") {
					t.Fatalf("websocket status = %q, err=%v", status, err)
				}
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatalf("read websocket headers: %v", err)
					}
					if line == "\r\n" {
						break
					}
				}
				return conn, reader
			}
			oldConn, oldReader := openWS(oldCookies)
			defer oldConn.Close()
			slot := 0
			if grouped {
				var err error
				slot, err = p.StageGroupedGeneration("dashboard", 202)
				if err != nil {
					t.Fatal(err)
				}
			} else if err := p.StageGeneration("dashboard", 202, 1); err != nil {
				t.Fatal(err)
			}
			if err := p.RegisterGenerationReplica("dashboard", 202, slot, v2.URL, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := p.ActivateGeneration("dashboard", 202); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(oldConn, "still-here\n")
			if got, err := oldReader.ReadString('\n'); err != nil || got != "v1:still-here\n" {
				t.Fatalf("old websocket after cutover = %q, err=%v", got, err)
			}
			if p.TryRetireGeneration("dashboard", 101) {
				t.Fatal("retired generation with an open websocket")
			}
			oldClientVersion := "v2"
			if grouped {
				oldClientVersion = "v1"
			}
			generationRequestCookies(t, p, oldCookies, oldClientVersion)
			// A reconnect uses the full browser cookie set. Grouped affinity is
			// the client-id cookie, not the informational replica cookie.
			_ = oldConn.Close()
			reconnected, reconnectedReader := openWS(oldCookies)
			defer reconnected.Close()
			_, _ = io.WriteString(reconnected, "reconnected\n")
			if got, err := reconnectedReader.ReadString('\n'); err != nil || got != oldClientVersion+":reconnected\n" {
				t.Fatalf("reconnected websocket = %q, err=%v", got, err)
			}
			generationRequestCookies(t, p, nil, "v2")
			newConn, newReader := openWS(nil)
			_, _ = io.WriteString(newConn, "new\n")
			if got, err := newReader.ReadString('\n'); err != nil || got != "v2:new\n" {
				t.Fatalf("new websocket after cutover = %q, err=%v", got, err)
			}
			_ = newConn.Close()
			_ = reconnected.Close()
			// Wait until reverse-proxy accounting observes the socket closes,
			// then confirm grouped bindings still protect the reconnect window.
			closedDeadline := time.Now().Add(2 * time.Second)
			for {
				sessions, _ := p.GenerationDrainingSessions("dashboard", 101)
				if sessions == 0 {
					break
				}
				if time.Now().After(closedDeadline) {
					t.Fatal("closed websocket retained an active connection")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if grouped && p.TryRetireGeneration("dashboard", 101) {
				t.Fatal("retired grouped clients before their reconnect grace expired")
			}
			// Exercise the real 15-second client grace timer. No manual worker
			// deregistration: idle retirement must follow natural binding expiry.
			deadline := time.Now().Add(20 * time.Second)
			for !p.TryRetireGeneration("dashboard", 101) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if p.IsGenerationDraining("dashboard", 101) {
				t.Fatal("old generation did not retire after its client grace expired")
			}
			generationRequestCookies(t, p, oldCookies, "v2")

		})
	}
}

func TestGenerationRetirementWaitsForActiveRequest(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	v1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			close(entered)
			<-release
		}
		_, _ = io.WriteString(w, "v1")
	}))
	defer v1.Close()
	v2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "v2")
	}))
	defer v2.Close()

	p := proxy.New()
	p.SetPoolSize("dashboard", 1)
	if err := p.RegisterReplica("dashboard", 0, v1.URL, nil, 101); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	oldCookie := generationRequest(t, p, nil, "v1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/app/dashboard/hold", nil)
		req.AddCookie(oldCookie)
		p.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-entered
	if err := p.StageGeneration("dashboard", 202, 1); err != nil {
		t.Fatalf("stage v2: %v", err)
	}
	if err := p.RegisterGenerationReplica("dashboard", 202, 0, v2.URL, nil); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if _, err := p.ActivateGeneration("dashboard", 202); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	if p.TryRetireGeneration("dashboard", 101) {
		t.Fatal("retired v1 while a request was still active")
	}
	if stat := p.PoolSessionSnapshot()["dashboard"]; stat.Sessions != 1 || stat.Replicas != 1 {
		t.Fatalf("handoff session snapshot = %+v, want one draining session and one admitting replica", stat)
	}
	if p.BeginHibernate("dashboard", time.Now().Add(time.Second)) {
		t.Fatal("hibernated app while a draining-generation request was active")
	}
	close(release)
	<-done
	if !p.TryRetireGeneration("dashboard", 101) {
		t.Fatal("v1 did not retire after its request drained")
	}
}

func generationRequest(t *testing.T, p *proxy.Proxy, cookie *http.Cookie, want string) *http.Cookie {
	t.Helper()
	var cookies []*http.Cookie
	if cookie != nil {
		cookies = append(cookies, cookie)
	}
	for _, c := range generationRequestCookies(t, p, cookies, want) {
		if c.Name == "shinyhub_rep_dashboard" {
			return c
		}
	}
	return cookie
}

func generationRequestCookies(t *testing.T, p *proxy.Proxy, cookies []*http.Cookie, want string) []*http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/app/dashboard/", nil)
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if res.StatusCode != http.StatusOK || string(body) != want {
		t.Fatalf("response = %d %q, want 200 %q", res.StatusCode, body, want)
	}
	return res.Cookies()
}
