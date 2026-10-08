package proxy_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

func TestAppBackendRedirectFollowPreservesRequest(t *testing.T) {
	for _, mode := range []string{"multiplex", "grouped"} {
		t.Run(mode, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/nested" {
					w.Header().Set("Location", "http://"+r.Host+"/app/demo/nested/?q=a%20b")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				body, _ := io.ReadAll(r.Body)
				fmt.Fprintf(w, "%s:%s:%s", r.Method, body, r.URL.RawQuery)
			}))
			defer backend.Close()
			front := httptest.NewServer(urlTestProxy(t, mode, backend.URL))
			defer front.Close()
			client := front.Client()
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client.Jar = jar
			resp, err := client.Post(front.URL+"/app/demo/nested", "text/plain", strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK || resp.Request.URL.String() != front.URL+"/app/demo/nested/?q=a%20b" || string(body) != "POST:payload:q=a%20b" {
				t.Fatalf("redirect lost request: status=%d url=%s body=%q", resp.StatusCode, resp.Request.URL, body)
			}
		})
	}
}

func urlTestProxy(t *testing.T, mode, target string) *proxy.Proxy {
	t.Helper()
	p := proxy.New()
	p.SetPoolSize("demo", 1)
	var err error
	if mode == "grouped" {
		p.SetPoolMode("demo", "grouped", 1, 1)
		err = p.RegisterElasticWorker("demo", 0, target, nil, 1)
	} else {
		err = p.Register("demo", target)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAppBackendRedirectsStayMounted(t *testing.T) {
	for _, mode := range []string{"multiplex", "grouped"} {
		for _, mount := range []string{"", "/v1/data/token", "/tunnel%20path/token"} {
			for _, tc := range []struct{ location, want string }{
				{"/nested/?x=%2f&q=a%20b#plot", "/app/demo/nested/?x=%2f&q=a%20b#plot"},
				{"/app/demo/nested/", "/app/demo/nested/"},
				{"../other/", "/app/demo/other/"},
				{"../../", "/app/demo/"},
				{"../../../other/", "/app/demo/other/"},
				{"child/", "/app/demo/nested/child/"},
				{"?x=%2f", "?x=%2f"},
				{"#plot", "#plot"},
				{"https://external.example/login", "https://external.example/login"},
				{"//external.example/login", "//external.example/login"},
				{"mailto:help@example.com", "mailto:help@example.com"},
				{"/files/a%2Fb?", "/app/demo/files/a%2Fb?"},
				{"@backend/app/demo/nested/", "/app/demo/nested/"},
				{"@backend" + mount + "/other/", "/app/demo/other/"},
			} {
				t.Run(mode+mount+"/"+tc.location, func(t *testing.T) {
					backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						location := tc.location
						if len(location) >= 8 && location[:8] == "@backend" {
							location = "http://" + r.Host + location[8:]
						}
						w.Header().Set("Location", location)
						w.WriteHeader(http.StatusTemporaryRedirect)
					}))
					defer backend.Close()
					p := urlTestProxy(t, mode, backend.URL+mount)
					w := httptest.NewRecorder()
					p.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/app/demo/nested/", nil))
					if w.Code != http.StatusTemporaryRedirect || w.Header().Get("Location") != tc.want {
						t.Fatalf("status=%d Location=%q, want 307 %q", w.Code, w.Header().Get("Location"), tc.want)
					}
				})
			}
		}
	}
}

func TestAppBackendPreservesEscapedPaths(t *testing.T) {
	for _, mode := range []string{"multiplex", "grouped"} {
		for _, mount := range []string{"", "/v1/data/token", "/tunnel%20path/token"} {
			for _, path := range []string{"/app/demo/files/a%2Fb", "/app/%64emo/files/a%2Fb", "/%61pp/demo/files/a%2Fb", "/app%2Fdemo/files/a%2Fb", "/app/demo%2Ffiles/a%2Fb"} {
				t.Run(mode+mount+path, func(t *testing.T) {
					backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.RequestURI) }))
					defer backend.Close()
					p := urlTestProxy(t, mode, backend.URL+mount)
					w := httptest.NewRecorder()
					p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"?x=%2f&state=a;b", nil))
					want := mount + "/files/a%2Fb?x=%2f&state=a;b"
					if w.Code != http.StatusOK || w.Body.String() != want {
						t.Fatalf("status=%d backendURI=%q, want %q", w.Code, w.Body.String(), want)
					}
				})
			}
		}
	}
}
