package main

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/ui"
)

// TestBrowserSessionRealBrowser executes production auth, launch exchanges,
// page injection and WebSocket deadlines in Chromium with isolated test users.
// The opt-in requires the repository's existing Playwright driver installation.
func TestBrowserSessionRealBrowser(t *testing.T) {
	if os.Getenv("SHINYHUB_BROWSER_SESSION_E2E") != "1" {
		t.Skip("set SHINYHUB_BROWSER_SESSION_E2E=1 to run Chromium session checks")
	}
	for _, isolated := range []bool{false, true} {
		t.Run(fmt.Sprintf("isolated=%v", isolated), func(t *testing.T) {
			store := dbtest.New(t)
			hash, err := auth.HashPassword("browser-fixture-password")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CreateUser(db.CreateUserParams{Username: "browser-fixture", PasswordHash: hash, Role: "admin"}); err != nil {
				t.Fatal(err)
			}
			u, err := store.GetUserByUsername("browser-fixture")
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.CreateApp(db.CreateAppParams{Slug: "session-app", Name: "Session app", OwnerID: u.ID})
			if err != nil {
				t.Fatal(err)
			}
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					conn, buf, err := w.(http.Hijacker).Hijack()
					if err != nil {
						return
					}
					defer conn.Close()
					sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
					fmt.Fprintf(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
					if err := buf.Flush(); err != nil {
						return
					}
					_, _ = io.Copy(io.Discard, buf)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'")
				io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Session app</title></head><body><label>Work <input id="work"></label><script>window.socket=new WebSocket(location.origin.replace('http','ws')+'/app/session-app/websocket');</script></body></html>`)
			}))
			defer backend.Close()
			prx := proxy.New()
			app, err := store.GetAppBySlug("session-app")
			if err != nil {
				t.Fatal(err)
			}
			prx.SetPoolAppID("session-app", app.ID)
			if err := prx.Register("session-app", backend.URL); err != nil {
				t.Fatal(err)
			}
			defer prx.DrainUpgraded(0)
			// Short times are fixture-only; production configuration validation
			// still requires at least one minute for each operator setting.
			ttl, maxAge := 4*time.Second, 12*time.Second
			cfg := &config.Config{Auth: config.AuthConfig{Secret: "browser-fixture-secret", SessionTTL: &ttl, SessionMaxAge: &maxAge}, Storage: config.StorageConfig{AppsDir: t.TempDir()}}
			srv := api.New(cfg, store, nil, prx)
			mux := http.NewServeMux()
			front := httptest.NewServer(mux)
			defer front.Close()
			controlURL := strings.Replace(front.URL, "127.0.0.1", "localhost", 1)
			cfg.Server.BaseURL = controlURL
			prx.SetBrowserSessions(maxAge, controlURL)
			mux.Handle("/api/", srv.Router())
			mux.Handle("/static/", ui.Handler())
			middleware := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, store.LookupContextUser)
			appHandler := middleware(prx)
			appURL := controlURL
			if isolated {
				appURL = front.URL
				origin, _ := url.Parse(appURL)
				appHandler = appOriginDispatch(origin, nil, store, cfg.Auth.Secret, middleware(appOriginRedirectHandler(store, origin, nil)), appHandler, cfg.Auth)
			}
			mux.Handle("/app/", appHandler)
			mux.Handle("GET /app/{slug}/.shinyhub/session.json", middleware(http.HandlerFunc(srv.HandleAppSession)))
			mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Session dashboard fixture</title></head><body><p id="status">Signed in</p><button id="logout">Sign out</button><script type="module">import {createSessionController} from '/static/views/session-controller.js'; const controller=createSessionController({request:fetch,onExpired:()=>document.getElementById('status').textContent='Signed out'}); const response=await fetch('/api/auth/me'); controller.start(await response.json()); document.getElementById('logout').onclick=async()=>{const csrf=document.cookie.split('; ').find(c=>c.startsWith('csrf_token='))?.split('=')[1]; const r=await fetch('/api/auth/logout',{method:'POST',headers:{'X-CSRF-Token':csrf}}); if(r.ok){controller.end();document.getElementById('status').textContent='Signed out';}};</script></body></html>`)
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go prx.StartSessionRecheck(ctx, 50*time.Millisecond, func(p proxy.ConnPrincipal) (bool, string, error) {
				return access.Recheck(store, store.LookupContextUser, store.IsTokenRevoked, access.Principal{Slug: p.Slug, UserID: p.UserID, Role: p.Role, SessionEpoch: p.SessionEpoch, JTI: p.JTI})
			})
			command := exec.CommandContext(t.Context(), "node", "../../scripts/browser-session-e2e.mjs", controlURL, appURL)
			output, err := command.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatalf("browser session checks: %v", err)
			}
		})
	}
}
