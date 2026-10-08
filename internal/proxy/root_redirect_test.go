package proxy_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestProxyAppRootRedirect(t *testing.T) {
	for _, registered := range []bool{true, false} {
		name := "dormant"
		if registered {
			name = "registered"
		}
		t.Run(name, func(t *testing.T) {
			p := proxy.New()
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("bare app root reached backend")
			}))
			defer backend.Close()
			if registered {
				if err := p.Register("demo", backend.URL); err != nil {
					t.Fatal(err)
				}
			} else {
				p.SetSlugExists(func(slug string) (bool, error) { return slug == "demo", nil })
			}
			var wakes atomic.Int64
			p.SetWakeTrigger(func(context.Context, string) { wakes.Add(1) })
			for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
				for _, query := range []string{"", "?", "?_inputs_&flt_app=%5B%22vscode%22%5D", "?x=1&x=2&q=a+b&encoded=%2f"} {
					req := httptest.NewRequest(method, "http://internal:8080/app/demo"+query, strings.NewReader("payload"))
					req.Header.Set("X-Forwarded-Proto", "https")
					req.Header.Set("X-Forwarded-Host", "public.example")
					rec := httptest.NewRecorder()
					p.ServeHTTP(rec, req)
					if rec.Code != http.StatusPermanentRedirect {
						t.Fatalf("%s %s: status = %d, want 308", method, req.URL, rec.Code)
					}
					if got, want := rec.Header().Get("Location"), "/app/demo/"+query; got != want {
						t.Errorf("Location = %q, want %q", got, want)
					}
					if len(rec.Header().Values("Set-Cookie")) != 0 {
						t.Error("redirect set routing cookies")
					}
					if method == http.MethodHead && rec.Body.Len() != 0 {
						t.Error("HEAD redirect has a body")
					}
				}
			}
			if wakes.Load() != 0 {
				t.Error("redirect triggered app wake")
			}
		})
	}
}

func TestProxyAppRootRedirectAssetsThroughAccess(t *testing.T) {
	const secret = "root-redirect-test-secret"
	for _, visibility := range []string{"public", "private"} {
		t.Run(visibility, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					w.Header().Set("Content-Type", "text/html")
					io.WriteString(w, `<script src="lib/shiny.js"></script><link rel="stylesheet" href="lib/style.css"><img src="icon.svg">`)
				case "/lib/shiny.js", "/lib/style.css", "/icon.svg":
					io.WriteString(w, r.URL.Path)
				default:
					http.NotFound(w, r)
				}
			}))
			defer backend.Close()
			p := proxy.New()
			if err := p.Register("demo", backend.URL); err != nil {
				t.Fatal(err)
			}
			st := chainStore{app: &db.App{ID: 1, Slug: "demo", Access: visibility}}
			lookup := func(id int64) (*auth.ContextUser, error) {
				return &auth.ContextUser{ID: id, Username: "test-user", Role: "developer"}, nil
			}
			mux := http.NewServeMux()
			mux.Handle("/app/", access.Middleware(st, secret, nil, lookup)(p))
			front := httptest.NewServer(mux)
			defer front.Close()
			client := front.Client()
			token, err := auth.IssueJWT(7, "test-user", "developer", secret)
			if err != nil {
				t.Fatal(err)
			}
			// Reattach the login cookie on every request, including redirects,
			// to exercise the authenticated post-login landing path.
			cookie := &http.Cookie{Name: auth.SessionCookieName, Value: token}
			client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
				if len(via) > 3 {
					return errors.New("redirect loop")
				}
				if visibility == "private" {
					req.Header.Del("Cookie")
					req.AddCookie(cookie)
				}
				return nil
			}
			get := func(target string) *http.Response {
				t.Helper()
				req, err := http.NewRequest(http.MethodGet, target, nil)
				if err != nil {
					t.Fatal(err)
				}
				if visibility == "private" {
					req.AddCookie(cookie)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}
			page := get(front.URL + "/app/demo?_inputs_&flt_app=%5B%22vscode%22%5D")
			body, _ := io.ReadAll(page.Body)
			page.Body.Close()
			if page.StatusCode != http.StatusOK || page.Request.URL.Path != "/app/demo/" || !strings.Contains(string(body), `src="lib/shiny.js"`) {
				t.Fatalf("unexpected landing page: status=%d url=%s body=%q", page.StatusCode, page.Request.URL, body)
			}
			// Resolve dependencies against the final document URL exactly as
			// browsers do, then fetch them through the same routing chain.
			for _, relative := range []string{"lib/shiny.js", "lib/style.css", "icon.svg"} {
				ref, _ := url.Parse(relative)
				assetURL := page.Request.URL.ResolveReference(ref)
				asset := get(assetURL.String())
				assetBody, _ := io.ReadAll(asset.Body)
				asset.Body.Close()
				if asset.StatusCode != http.StatusOK || string(assetBody) != "/"+relative {
					t.Errorf("asset %s: status=%d body=%q", assetURL, asset.StatusCode, assetBody)
				}
			}
		})
	}
}

func TestProxyAppRootRedirectOnlyKnownSlugs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lookup func(string) (bool, error)
		status int
	}{
		{"unknown", func(string) (bool, error) { return false, nil }, http.StatusNotFound},
		{"lookup error", func(string) (bool, error) { return false, errors.New("database unavailable") }, http.StatusOK},
		{"no lookup", nil, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := proxy.New()
			p.SetSlugExists(tc.lookup)
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/missing", nil))
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Errorf("unexpected redirect to %q", got)
			}
		})
	}
}

func TestProxyAppRootRedirectPreservesMethodAndAssets(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-App-Method", r.Method)
		w.Header().Set("X-App-Query", r.URL.RawQuery)
		w.Write(body)
	}))
	defer backend.Close()
	p := proxy.New()
	if err := p.Register("demo", backend.URL); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	defer front.Close()
	resp, err := front.Client().Post(front.URL+"/app/demo?x=%2f", "text/plain", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/app/demo/" ||
		resp.Header.Get("X-App-Method") != http.MethodPost || resp.Header.Get("X-App-Query") != "x=%2f" || string(body) != "payload" {
		t.Fatalf("redirect lost POST request: status=%d url=%s headers=%v body=%q", resp.StatusCode, resp.Request.URL, resp.Header, body)
	}
	for _, path := range []string{"/app/demo/", "/app/demo/lib/shiny.js", "/app/demo/www/icon.svg", "/app/demo/nested"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || rec.Header().Get("Location") != "" {
			t.Errorf("%s: status=%d Location=%q", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}
