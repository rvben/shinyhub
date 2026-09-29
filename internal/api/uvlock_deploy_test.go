package api_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

const lockHandlerLock = `version = 1
revision = 1

[[package]]
name = "app"
version = "0.1.0"
source = { virtual = "." }
dependencies = [
    { name = "six" },
]

[package.metadata]
requires-dist = [{ name = "six" }]

[[package]]
name = "six"
version = "1.17.0"
source = { registry = "https://mirror.example/simple" }
`

// A bundle whose uv.lock no longer records a requirement pyproject.toml
// declares is refused when it is uploaded, before the running pool is touched,
// on every runtime: each one installs the lock as-is. A current lock passes.
func TestDeployApp_RefusesStaleUVLock(t *testing.T) {
	for _, runtime := range []string{"native", "docker", "fargate"} {
		t.Run(runtime, func(t *testing.T) {
			appsDir := t.TempDir()
			store := dbtest.New(t)
			cfg := &config.Config{
				Auth:    config.AuthConfig{Secret: "test-secret"},
				Storage: config.StorageConfig{AppsDir: appsDir, VersionRetention: 5},
				Runtime: config.RuntimeConfig{
					Tiers: []config.TierConfig{{Name: "prod", Runtime: runtime}},
				},
			}
			mgr := process.NewManager(appsDir, process.NewNativeRuntime())
			srv := api.New(cfg, store, mgr, proxy.New())
			errReached := errors.New("stub: deploy hook reached")
			reached := false
			srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
				reached = true
				return nil, errReached
			})
			hash, _ := testHashPassword("pass")
			store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
			token, _ := auth.IssueJWT(1, "admin", "admin", "test-secret")
			createApp(t, srv, token, "demo")

			deployBundle := func(deps string) *httptest.ResponseRecorder {
				var zipBuf bytes.Buffer
				zw := zip.NewWriter(&zipBuf)
				for name, body := range map[string]string{
					"app.py":         "from shiny import App\n",
					"pyproject.toml": "[project]\nname = \"app\"\nversion = \"0.1.0\"\ndependencies = [" + deps + "]\n",
					"uv.lock":        lockHandlerLock,
				} {
					w, err := zw.Create(name)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := w.Write([]byte(body)); err != nil {
						t.Fatal(err)
					}
				}
				if err := zw.Close(); err != nil {
					t.Fatal(err)
				}
				var body bytes.Buffer
				mw := multipart.NewWriter(&body)
				part, _ := mw.CreateFormFile("bundle", "bundle.zip")
				part.Write(zipBuf.Bytes())
				mw.Close()
				req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", &body)
				req.Header.Set("Content-Type", mw.FormDataContentType())
				req.Header.Set("Authorization", "Bearer "+token)
				rr := httptest.NewRecorder()
				srv.Router().ServeHTTP(rr, req)
				return rr
			}

			rr := deployBundle(`"six", "idna"`)
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("stale lock: status = %d, want 422; body = %s", rr.Code, rr.Body.String())
			}
			for _, want := range []string{"idna", "uv lock"} {
				if !strings.Contains(rr.Body.String(), want) {
					t.Errorf("stale lock: body %s lacks %q", rr.Body.String(), want)
				}
			}
			if reached {
				t.Error("stale lock: the deploy hook ran")
			}

			if rr := deployBundle(`"six"`); !reached {
				t.Errorf("current lock was refused before the deploy hook: status %d, body %s", rr.Code, rr.Body.String())
			}
		})
	}
}
