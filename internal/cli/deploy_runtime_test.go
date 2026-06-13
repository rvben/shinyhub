package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestLooksLikeRApp(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"plain r app", []string{"app.R"}, true},
		{"python app", []string{"app.py", "requirements.txt"}, false},
		{"r app with manifest override is uncertain", []string{"app.R", "shinyhub.toml"}, false},
		{"both entrypoints", []string{"app.R", "app.py"}, false},
		{"empty dir", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := looksLikeRApp(dir); got != tc.want {
				t.Errorf("looksLikeRApp(%v) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}

func TestServerRuntimeAvailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"dev","runtimes":{"python":true,"r":false}}`))
	}))
	defer srv.Close()
	cfg := &cliConfig{Host: srv.URL}

	if avail, known := serverRuntimeAvailable(cfg, "r"); !known || avail {
		t.Errorf("r: got (avail=%v, known=%v), want (false, true)", avail, known)
	}
	if avail, known := serverRuntimeAvailable(cfg, "python"); !known || !avail {
		t.Errorf("python: got (avail=%v, known=%v), want (true, true)", avail, known)
	}
	if _, known := serverRuntimeAvailable(cfg, "go"); known {
		t.Errorf("go: unknown runtime should report known=false")
	}

	// An older server that does not report runtimes leaves the caller uncertain.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"dev"}`))
	}))
	defer old.Close()
	if _, known := serverRuntimeAvailable(&cliConfig{Host: old.URL}, "r"); known {
		t.Errorf("server without runtimes should report known=false")
	}
}
