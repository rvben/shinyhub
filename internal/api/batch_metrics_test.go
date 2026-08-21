package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker"
)

// GET /api/apps/metrics returns the requested apps' metrics keyed by slug in one
// response, skipping apps the caller cannot view and unknown slugs - so the
// dashboard populates every card with a single request.
func TestBatchMetrics_RequestedVisibleAppsKeyedBySlug(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateUser(db.CreateUserParams{Username: "other", PasswordHash: hash, Role: "developer"})
	other, _ := store.GetUserByUsername("other")

	store.CreateApp(db.CreateAppParams{Slug: "mine1", Name: "Mine 1", OwnerID: owner.ID})
	store.CreateApp(db.CreateAppParams{Slug: "mine2", Name: "Mine 2", OwnerID: owner.ID})
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Secret", OwnerID: other.ID}) // private, not visible to owner

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics?slugs=mine1,mine2,secret,ghost", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		GeneratedAt time.Time `json:"generated_at"`
		Metrics     map[string]struct {
			Status string `json:"status"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode (route should hit the batch handler, not handleGetApp): %v", err)
	}
	if body.GeneratedAt.IsZero() {
		t.Error("batch metrics must include generated_at")
	}
	if _, ok := body.Metrics["mine1"]; !ok {
		t.Error("mine1 missing from batch metrics")
	}
	if _, ok := body.Metrics["mine2"]; !ok {
		t.Error("mine2 missing from batch metrics")
	}
	if _, ok := body.Metrics["secret"]; ok {
		t.Error("another user's private app must not appear in batch metrics")
	}
	if _, ok := body.Metrics["ghost"]; ok {
		t.Error("an unknown slug must not appear in batch metrics")
	}
	if body.Metrics["mine1"].Status != "stopped" {
		t.Errorf("mine1 status = %q, want stopped (never deployed)", body.Metrics["mine1"].Status)
	}
}

// With no ?slugs=, the batch endpoint reports every app visible to the caller.
func TestBatchMetrics_NoSlugsReturnsAllVisible(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "a", Name: "A", OwnerID: owner.ID})
	store.CreateApp(db.CreateAppParams{Slug: "b", Name: "B", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Metrics map[string]json.RawMessage `json:"metrics"`
	}
	json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Metrics) != 2 {
		t.Fatalf("metrics count = %d, want 2 (all visible apps)", len(body.Metrics))
	}
}

// TestBatchMetrics_SurfacesLostReplicaAndAutoscale proves the batched replicas
// and latest autoscale event (fetched in bulk, not per card) flow through to
// each app's metrics: a lost replica shows "lost" + reason, and the autoscale
// status reflects the latest scale event. Guards the batch wiring into
// buildAppMetricsFrom, not just the status field.
func TestBatchMetrics_SurfacesLostReplicaAndAutoscale(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	reg, err := worker.NewRegistry(store)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	srv.SetWorkerRegistry(reg)

	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("demo")
	store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost, Tier: "remote"})
	store.LogAuditEvent(db.AuditEventParams{UserID: &owner.ID, Action: "autoscale_scale_up", ResourceType: "app", ResourceID: "demo", Detail: "1->2"})

	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics?slugs=demo", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Metrics map[string]struct {
			Replicas []struct {
				Index  int    `json:"index"`
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"replicas"`
			AutoscaleStatus *struct {
				LastAction string `json:"last_action"`
			} `json:"autoscale_status"`
		} `json:"metrics"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	demo, ok := body.Metrics["demo"]
	if !ok {
		t.Fatalf("demo missing from batch metrics: %s", rec.Body.String())
	}
	foundLost := false
	for _, r := range demo.Replicas {
		if r.Index == 0 {
			foundLost = true
			if r.Status != "lost" || r.Reason != "worker unavailable" {
				t.Errorf("batched replica: status=%q reason=%q, want lost/worker unavailable", r.Status, r.Reason)
			}
		}
	}
	if !foundLost {
		t.Errorf("batched lost replica missing: %s", rec.Body.String())
	}
	if demo.AutoscaleStatus == nil || demo.AutoscaleStatus.LastAction != "up" {
		t.Errorf("batched autoscale status = %+v, want last_action up", demo.AutoscaleStatus)
	}
}

// With no ?slugs=, an admin gets the whole server, matching what GET /api/apps
// hands the same caller. The two disagreeing is not cosmetic: an operator's
// dashboard lists every app, so a metrics set scoped to the operator's *own*
// apps leaves every card it does not own permanently blank, and `shinyhub top`
// would report an idle server to the one account able to see all of it.
func TestBatchMetrics_NoSlugsGivesAnAdminEveryApp(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateUser(db.CreateUserParams{Username: "root", PasswordHash: hash, Role: "admin"})
	admin, _ := store.GetUserByUsername("root")

	// Owned by somebody else and private, so only the admin role can reach it.
	store.CreateApp(db.CreateAppParams{Slug: "someone-elses", Name: "Theirs", OwnerID: owner.ID})

	token, _ := auth.IssueJWT(admin.ID, "root", "admin", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Metrics map[string]json.RawMessage `json:"metrics"`
	}
	json.NewDecoder(rec.Body).Decode(&body)
	if _, ok := body.Metrics["someone-elses"]; !ok {
		t.Errorf("admin got %v, want someone-elses: naming it in ?slugs= is answered, "+
			"so omitting ?slugs= must not hide it", keysOfRaw(body.Metrics))
	}
}

// Scope beats role and visibility everywhere else, and it must beat them here
// too. Omitting ?slugs= is the one path that never consults canViewApp, so
// without an explicit filter a token scoped to one app could read every public
// app's pids and resource usage simply by not naming them.
func TestBatchMetrics_NoSlugsHonoursTokenScope(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	for _, slug := range []string{"inscope", "outscope"} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID}); err != nil {
			t.Fatal(err)
		}
	}
	// Public, so every visibility rule short of scope would admit it.
	if err := store.SetAppAccess("outscope", "public"); err != nil {
		t.Fatal(err)
	}
	tok := scopedDeployToken(t, srv, store, "admin", []string{"inscope"})

	rec := doToken(t, srv, "GET", "/api/apps/metrics", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Metrics map[string]json.RawMessage `json:"metrics"`
	}
	json.NewDecoder(rec.Body).Decode(&body)
	if _, ok := body.Metrics["outscope"]; ok {
		t.Errorf("an out-of-scope public app leaked its metrics: got %v; ?slugs=outscope "+
			"refuses the same read", keysOfRaw(body.Metrics))
	}
	if _, ok := body.Metrics["inscope"]; !ok {
		t.Errorf("the in-scope app is missing: got %v, so this test would pass even if the "+
			"handler returned nothing at all", keysOfRaw(body.Metrics))
	}
}

// keysOfRaw names the slugs a metrics response carried, so a failure says which
// apps were reported rather than only how many.
func keysOfRaw(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The batch response carries the size of the box, because the Overview's
// resource panel has nothing to measure a limit-free fleet against otherwise.
// It rides this endpoint rather than its own so the denominator and the usage
// it scales always describe the same instant.
func TestBatchMetrics_PublishesHostCapacity(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	srv.SetHostCapacity(4, "cgroup-quota", 8192, "cgroup-limit")

	token, _ := auth.IssueJWT(ownerID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Host *struct {
			Cores        float64 `json:"cores"`
			CoresSource  string  `json:"cores_source"`
			MemoryMB     int     `json:"memory_mb"`
			MemorySource string  `json:"memory_source"`
		} `json:"host"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Host == nil {
		t.Fatalf("host capacity missing from the batch response: %s", rec.Body.String())
	}
	if body.Host.Cores != 4 || body.Host.MemoryMB != 8192 {
		t.Errorf("host = %+v, want 4 cores / 8192 MB", *body.Host)
	}
	// The source is what tells an operator whether the number describes their
	// container or the whole machine it happens to share.
	if body.Host.CoresSource != "cgroup-quota" || body.Host.MemorySource != "cgroup-limit" {
		t.Errorf("host sources = %q/%q, want cgroup-quota/cgroup-limit", body.Host.CoresSource, body.Host.MemorySource)
	}
}

// A host nobody could measure must be absent, not zero: the dashboard draws a
// meter from this number, and a 0-core host is a full bar at any usage.
func TestBatchMetrics_OmitsUnknownHostCapacity(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	srv.SetHostCapacity(0, "", 0, "")

	token, _ := auth.IssueJWT(ownerID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw, ok := body["host"]; ok {
		t.Errorf("host = %s, want the key absent when no source could size the box", raw)
	}
	// Positive control: the response is otherwise intact, so the absence above
	// is about the host block and not an empty body.
	if _, ok := body["metrics"]; !ok {
		t.Errorf("metrics missing too; this test proves nothing: %s", rec.Body.String())
	}
}

// Cores measured but memory not: the panel can scale CPU and must not invent a
// memory ceiling from the same block.
func TestBatchMetrics_HostCapacityOmitsUnknownMemory(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	srv.SetHostCapacity(2, "affinity", 0, "")

	token, _ := auth.IssueJWT(ownerID, "owner", "developer", "test-secret")
	req := authedRequest(t, "GET", "/api/apps/metrics", nil, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	var body struct {
		Host map[string]json.RawMessage `json:"host"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body.Host["cores"]; !ok {
		t.Fatalf("cores missing from a host that reported them: %s", rec.Body.String())
	}
	if raw, ok := body.Host["memory_mb"]; ok {
		t.Errorf("memory_mb = %s, want absent when only the core count is known", raw)
	}
}

// Unauthenticated callers are rejected.
func TestBatchMetrics_RequiresAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/api/apps/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an unauthenticated batch request", rec.Code)
	}
}
