package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

type groupedVersionTransport string

func (v groupedVersionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(strings.NewReader(string(v))), Request: req}, nil
}

func TestGroupedGenerationPreservesBindingsAndSeparatesNewClients(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationGrouped, 4, 1)
	p.SetSpawnFunc(func(string, int) { t.Error("ready candidate should avoid a cold spawn") })
	slot := p.reserveWorker("app", "")
	if err := p.RegisterElasticWorker("app", slot, "http://old/", groupedVersionTransport("old"), 10); err != nil {
		t.Fatal(err)
	}
	request := func(cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/app/app/", nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec
	}
	old := request(nil)
	if old.Body.String() != "old" {
		t.Fatalf("initial request: %s", old.Body.String())
	}
	candidate, err := p.StageGroupedGeneration("app", 20)
	if err != nil {
		t.Fatal(err)
	}
	if candidate <= slot {
		t.Fatal("candidate reused a live slot")
	}
	if _, err := p.ActivateGeneration("app", 20); err == nil {
		t.Fatal("unready candidate activated")
	}
	if err := p.RegisterGenerationReplica("app", 20, candidate, "http://new/", groupedVersionTransport("new")); err != nil {
		t.Fatal(err)
	}
	if got := request(nil).Body.String(); got != "old" {
		t.Fatalf("candidate leaked before activation: %s", got)
	}
	if previous, err := p.ActivateGeneration("app", 20); err != nil || previous != 10 {
		t.Fatalf("activate: %d %v", previous, err)
	}
	if got := request(old.Result().Cookies()).Body.String(); got != "old" {
		t.Fatalf("existing client changed versions: %s", got)
	}
	if got := request(nil).Body.String(); got != "new" {
		t.Fatalf("new client missed ready candidate: %s", got)
	}
	switchResponse := httptest.NewRecorder()
	p.ClearGenerationAffinity(switchResponse, httptest.NewRequest("POST", "/", nil), "app")
	if cookies := switchResponse.Result().Cookies(); len(cookies) != 2 || request(cookies).Body.String() != "new" {
		t.Fatal("explicit version switch did not reset grouped client affinity")
	}
	p.mu.RLock()
	p.pools["app"].workers[slot].activeConns.Store(1)
	p.pools["app"].workers[candidate].activeConns.Store(2)
	p.mu.RUnlock()
	if stat := p.PoolSessionSnapshot()["app"]; stat.Sessions != 3 || stat.Replicas != 1 || stat.Cap != 4 {
		t.Fatalf("grouped capacity double-counted old workers: %+v", stat)
	}
	p.mu.RLock()
	p.pools["app"].workers[slot].activeConns.Store(0)
	p.pools["app"].workers[candidate].activeConns.Store(0)
	p.mu.RUnlock()
	if snap, _ := p.ElasticWorkersSnapshot("app"); snap.Workers[0].Status != "draining" {
		t.Fatal("old grouped worker not reported as draining")
	}
	if p.TryRetireGeneration("app", 10) {
		t.Fatal("retired a generation with reconnectable client bindings")
	}
	if p.ElasticSlotCanStart("app", slot) {
		t.Fatal("old slot accepts a delayed spawn")
	}
	p.DeregisterElasticWorker("app", slot)
	if !p.TryRetireGeneration("app", 10) {
		t.Fatal("unbound idle generation did not retire")
	}
	if got := request(old.Result().Cookies()).Body.String(); got != "new" {
		t.Fatalf("retired client did not migrate: %s", got)
	}
}

func TestGroupedGenerationAbortLeavesOldWorkersAndDoesNotReuseSlots(t *testing.T) {
	p := New()
	p.SetPoolMode("app", config.IsolationGrouped, 4, 2)
	slot, err := p.StageGroupedGeneration("app", 20)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AbortGeneration("app", 20) {
		t.Fatal("abort failed")
	}
	next, err := p.StageGroupedGeneration("app", 30)
	if err != nil || next <= slot {
		t.Fatalf("slot reused after abort: %d %d %v", slot, next, err)
	}
}

func TestGroupedGenerationHTMLKeepsItsOwnVersionToken(t *testing.T) {
	p := New()
	p.SetAppNav(true, "https://hub.example.com/")
	p.SetPoolMode("app", config.IsolationGrouped, 4, 2)
	p.SetGenerationActivationToken("app", 10, "old-opaque-token")
	p.SetGenerationActivationToken("app", 20, "new-opaque-token")
	slot := p.reserveWorker("app", "")
	if err := p.RegisterElasticWorker("app", slot, "http://old/", groupedVersionTransport("<html><head></head><body>old</body></html>"), 10); err != nil {
		t.Fatal(err)
	}
	request := func(cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/app/app/", nil)
		req.Header.Set("Accept", "text/html")
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)
		return rec
	}
	initial := request(nil)
	candidate, err := p.StageGroupedGeneration("app", 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.RegisterGenerationReplica("app", 20, candidate, "http://new/", groupedVersionTransport("<html><head></head><body>new</body></html>")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ActivateGeneration("app", 20); err != nil {
		t.Fatal(err)
	}
	if html := request(initial.Result().Cookies()).Body.String(); !strings.Contains(html, "old-opaque-token") || strings.Contains(html, "new-opaque-token") {
		t.Fatalf("old page lost its generation identity: %s", html)
	}
	if html := request(nil).Body.String(); !strings.Contains(html, "new-opaque-token") {
		t.Fatalf("new page missing generation identity: %s", html)
	}
}
