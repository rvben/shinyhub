package api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/trustedpublish"
)

func TestTrustedPublishingExchangeScopeReplayAndRevocation(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
	}))
	defer issuer.Close()
	p := trustedpublish.Policy{Name: "production", Issuer: issuer.URL, JWKSURL: issuer.URL + "/keys", Audience: "https://hub.example", Subject: "workload:production", Claims: map[string]string{"project_id": "123"}, Apps: []string{"sales"}}
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret", TrustedPublishers: []trustedpublish.Policy{p}}, Storage: config.StorageConfig{AppsDir: t.TempDir()}}
	store := dbtest.New(t)
	server := New(cfg, store, nil, nil)
	server.trustedVerifier = trustedpublish.NewVerifier(issuer.Client())
	sign := func(id string) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": p.Issuer, "aud": p.Audience, "sub": p.Subject, "jti": id, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(), "project_id": "123"})
		token.Header["kid"] = "test"
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	exchange := func(assertion string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"policy": p.Name, "identity_token": assertion})
		rr := httptest.NewRecorder()
		server.Router().ServeHTTP(rr, httptest.NewRequest("POST", "/api/auth/trusted-publishing", bytes.NewReader(body)))
		return rr
	}
	assertion := sign("first")
	response := exchange(assertion)
	if response.Code != 201 {
		t.Fatalf("exchange=%d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential response is cacheable")
	}
	var credential struct {
		Token   string    `json:"token"`
		ID      int64     `json:"id"`
		Expires time.Time `json:"expires_at"`
	}
	json.Unmarshal(response.Body.Bytes(), &credential)
	if delta := time.Until(credential.Expires); delta < 9*time.Minute || delta > 11*time.Minute {
		t.Fatalf("lifetime=%s", delta)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Token "+credential.Token)
		rr := httptest.NewRecorder()
		server.Router().ServeHTTP(rr, r)
		return rr
	}
	if r := request("GET", "/api/auth/me", ""); r.Code != 200 {
		t.Fatalf("me=%d %s", r.Code, r.Body.String())
	}
	if r := request("POST", "/api/apps", `{"slug":"other","name":"Other"}`); r.Code != 403 {
		t.Fatalf("out-of-scope create=%d %s", r.Code, r.Body.String())
	}
	if r := request("POST", "/api/apps", `{"slug":"sales","name":"Sales"}`); r.Code != 201 {
		t.Fatalf("scoped create=%d %s", r.Code, r.Body.String())
	}
	if r := request("POST", "/api/tokens", `{"name":"persist"}`); r.Code != 403 {
		t.Fatalf("token persistence=%d", r.Code)
	}
	if r := request("GET", "/api/users", ""); r.Code != 403 {
		t.Fatalf("admin access=%d", r.Code)
	}
	if r := exchange(assertion); r.Code != 409 {
		t.Fatalf("replay=%d", r.Code)
	}
	cfg.Auth.TrustedPublishers = nil
	if r := request("GET", "/api/auth/me", ""); r.Code != 401 {
		t.Fatalf("removed policy=%d", r.Code)
	}
	cfg.Auth.TrustedPublishers = []trustedpublish.Policy{p}
	account, accountErr := store.GetServiceAccount("deployment")
	if accountErr != nil {
		t.Fatal(accountErr)
	}
	if err := store.DeleteServiceCredential(credential.ID, account.ID); err != nil {
		t.Fatal(err)
	}
	if r := exchange(assertion); r.Code != 409 {
		t.Fatalf("replay after deletion=%d", r.Code)
	}
	// Concurrent exchanges must have exactly one winner, as on separate HA nodes.
	assertion = sign("parallel")
	codes := make(chan int, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- exchange(assertion).Code }()
	}
	wg.Wait()
	close(codes)
	wins := 0
	for code := range codes {
		if code == 201 {
			wins++
		} else if code != 409 {
			t.Errorf("parallel exchange=%d", code)
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
}

func TestRuntimeCapabilitiesUseProducerAndIsolationGuards(t *testing.T) {
	store := dbtest.New(t)
	cfg := &config.Config{Storage: config.StorageConfig{AppsDir: t.TempDir()}}
	s := New(cfg, store, nil, nil)
	read := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.handleRuntimeCapabilities(rr, httptest.NewRequest("GET", "/api/runtime-capabilities", nil))
		return rr
	}
	var report struct {
		Features            map[string]runtimeCapability `json:"features"`
		RequiresHostRuntime bool                         `json:"requires_host_runtime"`
	}
	json.Unmarshal(read().Body.Bytes(), &report)
	if !report.RequiresHostRuntime {
		t.Fatal("native placement must check host launchers")
	}
	if !report.Features["deploy_producers"].Supported || !report.Features["data_activation"].Supported {
		t.Fatalf("new native app=%+v", report.Features)
	}
	cfg.Runtime.Mode = "docker"
	json.Unmarshal(read().Body.Bytes(), &report)
	if report.RequiresHostRuntime {
		t.Fatal("container placement must not depend on host launchers")
	}
	if report.Features["deploy_producers"].Supported || report.Features["data_activation"].Supported {
		t.Fatal("Docker producer advertised")
	}
	cfg.Runtime.Mode = "native"
	s.clustered = true
	json.Unmarshal(read().Body.Bytes(), &report)
	if report.Features["grouped"].Supported || report.Features["data_activation"].Supported {
		t.Fatal("cluster restrictions not advertised")
	}
	if !report.Features["multiplex"].Supported {
		t.Fatal("multiplex rejected")
	}
	s.nodeForTier = func(string) string { return "remote-worker" }
	json.Unmarshal(read().Body.Bytes(), &report)
	if report.RequiresHostRuntime {
		t.Fatal("remote workers must not depend on control-plane launchers")
	}
}
