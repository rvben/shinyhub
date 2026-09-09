package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

type elasticTestDialer struct {
	client *http.Client
	base   string
}

func (d elasticTestDialer) DialWorker(db.Worker) (*http.Client, string, error) {
	return d.client, d.base, nil
}

// Drop only after the server has completed the operation. This represents an
// unknown remote outcome, not a failure to send the request.
type elasticDropResponse struct {
	next http.RoundTripper
	path string
}

func (d *elasticDropResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := d.next.RoundTrip(r)
	if err == nil && r.URL.Path == d.path {
		d.path = ""
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return nil, errors.New("response lost after worker execution")
	}
	return response, err
}

type elasticClientFixture struct {
	store       *db.Store
	server      *replicaServer
	runtime     *elasticTestRuntime
	launcher    *ElasticLauncher
	drop        *elasticDropResponse
	owner       db.ElasticOwner
	reservation db.ElasticReservation
	worker      db.Worker
	request     api.ReplicaStartRequest
}

func newElasticClientFixture(t *testing.T) *elasticClientFixture {
	t.Helper()
	store := dbtest.New(t)
	ownerID := mustSeedOwner(t, store)
	_, err := store.CreateApp(db.CreateAppParams{Slug: "isolated", Name: "isolated", OwnerID: ownerID, Access: "private"})
	if err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("isolated")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := store.BeginDeployment(app.ID, "one", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET worker_isolation='per_session',worker_max_workers=1,status='running' WHERE id=?`, app.ID); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := store.AcquireOwner("controller-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("owner: %v %v", ok, err)
	}
	owner := db.ElasticOwner{Instance: "controller-a", Epoch: epoch}
	reservation, err := store.ReserveElasticSession(context.Background(), owner, app.ID, dep.ID, "client")
	if err != nil {
		t.Fatal(err)
	}
	rt := &elasticTestRuntime{containers: make(map[string]process.ContainerInfo)}
	srv := NewReplicaServer(ReplicaServerConfig{Runtime: rt, DataDir: t.TempDir(), NodeID: "node-a", Advertise: "worker:8443", AllocatePort: func() int { return 49001 }, AuthorizeElastic: func(ctx context.Context, a api.ElasticAuthority) error {
		return store.AuthorizeElasticCommand(ctx, db.ElasticOwner{Instance: a.Instance, Epoch: a.Epoch}, a.ReservationID, a.NodeID, a.RequestDigest, a.Action)
	}})
	t.Cleanup(srv.fenceElastic)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/elastic/prepare", srv.handleElasticPrepare)
	mux.HandleFunc("/v1/elastic/ack", srv.handleElasticAck)
	mux.HandleFunc("/v1/elastic/stop", srv.handleElasticStop)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	drop := &elasticDropResponse{next: httpServer.Client().Transport}
	launcher := &ElasticLauncher{Store: store, Dialer: elasticTestDialer{client: &http.Client{Transport: drop}, base: httpServer.URL}}
	return &elasticClientFixture{store: store, server: srv, runtime: rt, launcher: launcher, drop: drop, owner: owner, reservation: *reservation, worker: db.Worker{NodeID: "node-a"}, request: api.ReplicaStartRequest{Slug: app.Slug, Index: reservation.Slot, DeploymentID: dep.ID, Command: []string{"./server"}, BindPort: 8080}}
}
func (f *elasticClientFixture) launch() (*api.ElasticResult, error) {
	return f.launcher.Launch(context.Background(), f.owner, f.reservation, f.worker, f.request)
}
func (f *elasticClientFixture) stop() error {
	return f.launcher.Stop(context.Background(), f.owner, f.reservation.ID, f.worker, api.ElasticRequestDigest(f.request))
}
func (f *elasticClientFixture) capacity(t *testing.T, blocked bool) {
	t.Helper()
	_, err := f.store.ReserveElasticSession(context.Background(), f.owner, f.reservation.AppID, f.reservation.DeploymentID, "replacement")
	if blocked && !errors.Is(err, db.ErrElasticCapacity) {
		t.Fatalf("capacity released before stop confirmation: %v", err)
	}
	if !blocked && err != nil {
		t.Fatalf("confirmed stop retained capacity: %v", err)
	}
}
func (f *elasticClientFixture) runtimeState(t *testing.T, starts, executions, containers int) {
	t.Helper()
	f.server.elasticMu.Lock()
	defer f.server.elasticMu.Unlock()
	if f.runtime.starts != starts || f.runtime.executions != executions || len(f.runtime.containers) != containers {
		t.Fatalf("runtime starts=%d executions=%d containers=%d; want %d %d %d", f.runtime.starts, f.runtime.executions, len(f.runtime.containers), starts, executions, containers)
	}
}
func TestElasticClientLaunchAndStop(t *testing.T) {
	f := newElasticClientFixture(t)
	first, err := f.launch()
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.launch()
	if err != nil {
		t.Fatal(err)
	}
	if *first != *second || first.State != "ready" {
		t.Fatalf("launch replay changed result: %+v %+v", first, second)
	}
	f.runtimeState(t, 1, 1, 1)
	f.capacity(t, true)
	if err := f.stop(); err != nil {
		t.Fatal(err)
	}
	if err := f.stop(); err != nil {
		t.Fatalf("stop replay: %v", err)
	}
	f.runtimeState(t, 1, 1, 0)
	f.capacity(t, false)
}
func TestElasticClientLostLaunchResponsesRetrySameWorker(t *testing.T) {
	for _, action := range []string{"prepare", "ack"} {
		t.Run(action, func(t *testing.T) {
			f := newElasticClientFixture(t)
			f.drop.path = "/v1/elastic/" + action
			if _, err := f.launch(); err == nil {
				t.Fatal("lost response reported launch success")
			}
			f.capacity(t, true)
			executions := 0
			if action == "ack" {
				executions = 1
			}
			f.runtimeState(t, 1, executions, 1)
			result, err := f.launch()
			if err != nil {
				t.Fatal(err)
			}
			if result.State != "ready" {
				t.Fatalf("retry: %+v", result)
			}
			f.runtimeState(t, 1, 1, 1)
			if err := f.stop(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestElasticClientLostStopResponseRetainsCapacity(t *testing.T) {
	f := newElasticClientFixture(t)
	if _, err := f.launch(); err != nil {
		t.Fatal(err)
	}
	f.drop.path = "/v1/elastic/stop"
	if err := f.stop(); err == nil {
		t.Fatal("lost stop response reported success")
	}
	f.runtimeState(t, 1, 1, 0)
	f.capacity(t, true)
	if err := f.stop(); err != nil {
		t.Fatal(err)
	}
	f.capacity(t, false)
}
func TestElasticClientTakeoverStopsOriginalWorker(t *testing.T) {
	f := newElasticClientFixture(t)
	if _, err := f.launch(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReleaseOwner(f.owner.Instance, f.owner.Epoch); err != nil {
		t.Fatal(err)
	}
	ok, epoch, err := f.store.AcquireOwner("controller-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("takeover: %v %v", ok, err)
	}
	if _, err := f.launch(); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("stale launch: %v", err)
	}
	if err := f.stop(); !errors.Is(err, db.ErrElasticFenced) {
		t.Fatalf("stale stop: %v", err)
	}
	f.owner = db.ElasticOwner{Instance: "controller-b", Epoch: epoch}
	f.capacity(t, true)
	if err := f.stop(); err != nil {
		t.Fatal(err)
	}
	f.runtimeState(t, 1, 1, 0)
	f.capacity(t, false)
}
func TestElasticClientUnknownLaunchCannotMoveWorkers(t *testing.T) {
	f := newElasticClientFixture(t)
	f.drop.path = "/v1/elastic/prepare"
	if _, err := f.launch(); err == nil {
		t.Fatal("expected unknown launch result")
	}
	original := f.worker
	f.worker.NodeID = "node-b"
	if _, err := f.launch(); !errors.Is(err, db.ErrElasticConflict) {
		t.Fatalf("worker reassignment: %v", err)
	}
	f.runtimeState(t, 1, 0, 1)
	f.capacity(t, true)
	f.worker = original
	if err := f.stop(); err != nil {
		t.Fatal(err)
	}
	f.runtimeState(t, 1, 0, 0)
	f.capacity(t, false)
}
