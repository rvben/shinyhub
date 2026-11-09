package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestAppAvailabilityPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, status                                             string
		deployment                                               int64
		serving, eligible, blocked, activationBlocked, available bool
		wantStatus, basis, reason                                string
	}{
		{name: "running", status: "running", deployment: 1, serving: true, available: true, basis: "serving_route", wantStatus: "running"},
		{name: "partial capacity", status: "degraded", deployment: 1, serving: true, available: true, basis: "serving_route", wantStatus: "degraded"},
		{name: "asleep", status: "hibernated", deployment: 1, available: true, basis: "activation_eligible", wantStatus: "hibernated"},
		{name: "elastic idle", status: "running", deployment: 1, eligible: true, available: true, basis: "activation_eligible", wantStatus: "running"},
		{name: "old release during deployment", status: "deploying", deployment: 1, serving: true, available: true, basis: "serving_route", wantStatus: "deploying"},
		{name: "elastic old release during deployment", status: "deploying", deployment: 1, eligible: true, available: true, basis: "activation_eligible", wantStatus: "deploying"},
		{name: "elastic old release during restart", status: "starting", deployment: 1, eligible: true, available: true, basis: "activation_eligible", wantStatus: "starting"},
		{name: "elastic deployment with compatibility fence", status: "deploying", deployment: 1, eligible: true, activationBlocked: true, wantStatus: "deploying", reason: "activation_blocked"},
		{name: "initial startup", status: "starting", deployment: 1, wantStatus: "starting", reason: "no_serving_capacity"},
		{name: "waking", status: "waking", deployment: 1, available: true, basis: "activation_eligible", wantStatus: "waking"},
		{name: "hibernating", status: "hibernating", deployment: 1, available: true, basis: "activation_eligible", wantStatus: "hibernating"},
		{name: "blocked wake", status: "waking", deployment: 1, activationBlocked: true, wantStatus: "waking", reason: "activation_blocked"},
		{name: "no deployment", status: "hibernated", wantStatus: "hibernated", reason: "no_serving_release"},
		{name: "stopped with stale route", status: "stopped", deployment: 1, serving: true, wantStatus: "stopped"},
		{name: "crashed", status: "crashed", deployment: 1, serving: true, wantStatus: "crashed"},
		{name: "failed", status: "failed", deployment: 1, serving: true, wantStatus: "failed"},
		{name: "quarantined stale route", status: "running", deployment: 1, serving: true, blocked: true, wantStatus: "quarantined", reason: "compatibility_blocked"},
		{name: "quarantined stopped app", status: "stopped", deployment: 1, blocked: true, wantStatus: "quarantined", reason: "compatibility_blocked"},
		{name: "publisher blocks wake", status: "hibernated", deployment: 1, activationBlocked: true, wantStatus: "hibernated", reason: "activation_blocked"},
		{name: "publisher permits existing consumer", status: "running", deployment: 1, serving: true, activationBlocked: true, available: true, basis: "serving_route", wantStatus: "running"},
		{name: "publisher blocks elastic allocation", status: "running", deployment: 1, eligible: true, activationBlocked: true, wantStatus: "running", reason: "activation_blocked"},
		{name: "unknown stored status", status: "sensitive diagnostic", deployment: 1, serving: true, wantStatus: "unknown", reason: "state_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluateAppAvailability(db.AppAvailability{Status: tc.status, DeploymentID: tc.deployment, ServingBlocked: tc.blocked, ActivationBlocked: tc.activationBlocked}, tc.serving, tc.eligible)
			if got.Available != tc.available || got.Status != tc.wantStatus || got.Basis != tc.basis || got.Reason != tc.reason {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestAppAvailabilityHTTPIsAnonymousAndNonWaking(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared origin", true: "isolated origin"}[isolated], func(t *testing.T) {
			h := newTestHTTPHandler(t, func(cfg *config.Config) {
				cfg.Server.PublicAppAvailability = []string{"demo", "missing"}
				cfg.Auth.ForwardAuth.Enabled = true
				if isolated {
					cfg.Server.AppOrigin = "https://apps.example.com"
				}
			})
			close(h.readyCh)
			h.prx.MarkSynced()
			if err := h.store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "developer"}); err != nil {
				t.Fatal(err)
			}
			u, err := h.store.GetUserByUsername("owner")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Private", OwnerID: u.ID, Access: "private"}); err != nil {
				t.Fatal(err)
			}
			app, err := h.store.GetAppBySlug("demo")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "good", BundleDir: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
			if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "hibernated"}); err != nil {
				t.Fatal(err)
			}
			var wakes atomic.Int64
			h.prx.SetWakeTrigger(func(context.Context, string) { wakes.Add(1) })
			request := func(method, path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "https://apps.example.com"+path, nil)
				rec := httptest.NewRecorder()
				h.handler.ServeHTTP(rec, req)
				return rec
			}
			path := "/app/demo/.shinyhub/availability"
			before, err := h.store.GetAppBySlug("demo")
			if err != nil {
				t.Fatal(err)
			}
			idle := h.prx.IdleSince("demo")
			for range 25 {
				rec := request("GET", path)
				var body appAvailabilityResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if rec.Code != 200 || !body.Available || body.Status != "hibernated" || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Set-Cookie") != "" {
					t.Fatalf("probe: %d %s headers=%v", rec.Code, rec.Body, rec.Header())
				}
			}
			after, err := h.store.GetAppBySlug("demo")
			if err != nil {
				t.Fatal(err)
			}
			if wakes.Load() != 0 || after.Status != "hibernated" || !before.UpdatedAt.Equal(after.UpdatedAt) || !h.prx.IdleSince("demo").Equal(idle) || !h.prx.LastSeen("demo").IsZero() || h.prx.PoolHasAny("demo") {
				t.Fatal("polling changed lifecycle or activity")
			}
			if rec := request("HEAD", path); rec.Code != 200 || rec.Body.Len() != 0 {
				t.Fatalf("HEAD: %d %s", rec.Code, rec.Body)
			}
			if rec := request("POST", path); rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("POST: %d", rec.Code)
			}
			if _, err := h.store.CreateApp(db.CreateAppParams{Slug: "unlisted", Name: "Unlisted private app", OwnerID: u.ID, Access: "private"}); err != nil {
				t.Fatal(err)
			}
			for _, slug := range []string{"missing", "unlisted"} {
				if rec := request("GET", "/app/"+slug+"/.shinyhub/availability"); rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("%s: %d", slug, rec.Code)
				}
			}
			for _, protected := range []string{"/app/demo/", "/app/demo/.shinyhub/ready", path + "/extra"} {
				if rec := request("GET", protected); rec.Code != 401 {
					t.Fatalf("auth changed for %s: %d", protected, rec.Code)
				}
			}
			// Running probes must not keep an otherwise idle app awake either.
			if err := h.prx.Register("demo", "http://127.0.0.1:9999"); err != nil {
				t.Fatal(err)
			}
			if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "running"}); err != nil {
				t.Fatal(err)
			}
			runningIdle := h.prx.IdleSince("demo")
			for range 25 {
				if rec := request("GET", path); rec.Code != 200 {
					t.Fatalf("running probe: %d %s", rec.Code, rec.Body)
				}
			}
			if !h.prx.IdleSince("demo").Equal(runningIdle) || !h.prx.LastSeen("demo").IsZero() || wakes.Load() != 0 {
				t.Fatal("running probes changed activity")
			}
			// The control origin serves the same monitoring response without a launch redirect.
			control := httptest.NewRecorder()
			h.handler.ServeHTTP(control, httptest.NewRequest("GET", "http://127.0.0.1"+path, nil))
			if control.Code != 200 || control.Header().Get("Location") != "" || control.Header().Get("Set-Cookie") != "" {
				t.Fatalf("control origin: %d %v", control.Code, control.Header())
			}
			if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "stopped"}); err != nil {
				t.Fatal(err)
			}
			if rec := request("GET", path); rec.Code != 503 {
				t.Fatalf("stopped app with stale route: %d %s", rec.Code, rec.Body)
			}
			h.prx.SetDraining(true)
			if rec := request("GET", path); rec.Code != 503 {
				t.Fatalf("draining: %d", rec.Code)
			}
		})
	}
}

type unavailableAvailabilityStore struct{ t *testing.T }

func (s unavailableAvailabilityStore) GetAppAvailability(ctx context.Context, _ string) (*db.AppAvailability, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Second {
		s.t.Fatal("query lacks bounded deadline")
	}
	return nil, errors.New("secret database connection detail")
}

func TestAppAvailabilityUnavailableStateDoesNotLeak(t *testing.T) {
	h := newTestHTTPHandler(t, nil)
	mux := http.NewServeMux()
	mux.Handle("/app/{slug}/.shinyhub/availability", appAvailabilityHandler([]string{"demo"}, unavailableAvailabilityStore{t}, h.prx, h.readyCh))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/.shinyhub/availability", nil))
	if rec.Code != 503 || rec.Body.String() != "{\"available\":false,\"status\":\"unknown\",\"reason\":\"state_unavailable\"}\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := h.get(t, "/app/demo/.shinyhub/availability", nil); rec.Code != 404 {
		t.Fatalf("disabled: %d", rec.Code)
	}
}

type availabilityStoreFunc func(context.Context, string) (*db.AppAvailability, error)

func (f availabilityStoreFunc) GetAppAvailability(ctx context.Context, slug string) (*db.AppAvailability, error) {
	return f(ctx, slug)
}

func TestAppAvailabilityPlatformGates(t *testing.T) {
	ready := make(chan struct{})
	p := proxy.New()
	store := availabilityStoreFunc(func(context.Context, string) (*db.AppAvailability, error) {
		return &db.AppAvailability{Status: "hibernated", DeploymentID: 42}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("/app/{slug}/.shinyhub/availability", appAvailabilityHandler([]string{"demo"}, store, p, ready))
	check := func(wantCode int, basis, reason string) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/.shinyhub/availability", nil))
		var body appAvailabilityResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != wantCode || body.Reason != reason || body.Basis != basis || body.Available != (wantCode == 200) {
			t.Fatalf("%d %+v", rec.Code, body)
		}
	}
	check(503, "", "platform_unavailable")
	close(ready)
	check(503, "", "platform_unavailable")
	p.MarkSynced()
	check(200, "activation_eligible", "")
	p.SetDraining(true)
	check(503, "", "platform_unavailable")
}

func TestAppAvailabilityBoundsLookupsAcrossSlugsAndReleasesCanceledRequests(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	p := proxy.New()
	p.MarkSynced()
	started := make(chan struct{}, appAvailabilityConcurrency)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int64
	store := availabilityStoreFunc(func(ctx context.Context, _ string) (*db.AppAvailability, error) {
		if calls.Add(1) <= appAvailabilityConcurrency {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &db.AppAvailability{Status: "hibernated", DeploymentID: 1}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("/app/{slug}/.shinyhub/availability", appAvailabilityHandler([]string{"demo", "other"}, store, p, ready))
	request := func(ctx context.Context, method, slug string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, "/app/"+slug+"/.shinyhub/availability", nil).WithContext(ctx))
		return rec
	}
	var firstCancel context.CancelFunc
	var firstDone chan *httptest.ResponseRecorder
	var cancels []context.CancelFunc
	var workers sync.WaitGroup
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
		workers.Wait()
	}()
	for i := range appAvailabilityConcurrency {
		ctx, cancel := context.WithCancel(context.Background())
		cancels = append(cancels, cancel)
		done := make(chan *httptest.ResponseRecorder, 1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			done <- request(ctx, "GET", "demo")
		}()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("lookup failed to start")
		}
		if i == 0 {
			firstCancel, firstDone = cancel, done
		}
	}
	for _, method := range []string{"GET", "HEAD"} {
		rec := request(context.Background(), method, "other")
		if rec.Code != 429 || rec.Header().Get("Retry-After") != "1" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("saturated %s: %d %v %s", method, rec.Code, rec.Header(), rec.Body)
		}
		if method == "HEAD" && rec.Body.Len() != 0 {
			t.Fatal("HEAD overload response has a body")
		}
	}
	if calls.Load() != appAvailabilityConcurrency {
		t.Fatal("excess probe reached database")
	}
	if rec := request(context.Background(), "GET", "private"); rec.Code != 404 {
		t.Fatalf("unlisted while busy: %d", rec.Code)
	}
	if rec := request(context.Background(), "POST", "demo"); rec.Code != 405 {
		t.Fatalf("method check while busy: %d", rec.Code)
	}
	firstCancel()
	select {
	case rec := <-firstDone:
		if rec.Code != 503 {
			t.Fatalf("canceled probe: %d", rec.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled probe held lookup slot")
	}
	if rec := request(context.Background(), "GET", "other"); rec.Code != 200 {
		t.Fatalf("slot not recovered: %d %s", rec.Code, rec.Body)
	}
}

type availabilityBlockingWriter struct {
	*httptest.ResponseRecorder
	started chan<- struct{}
	release <-chan struct{}
}

func (w availabilityBlockingWriter) Write(body []byte) (int, error) {
	w.started <- struct{}{}
	<-w.release
	return w.ResponseRecorder.Write(body)
}

func TestAppAvailabilitySlowResponsesDoNotHoldLookupBudget(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	p := proxy.New()
	p.MarkSynced()
	store := availabilityStoreFunc(func(context.Context, string) (*db.AppAvailability, error) {
		return &db.AppAvailability{Status: "hibernated", DeploymentID: 1}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("/app/{slug}/.shinyhub/availability", appAvailabilityHandler([]string{"demo"}, store, p, ready))
	started := make(chan struct{}, appAvailabilityConcurrency)
	release := make(chan struct{})
	var workers sync.WaitGroup
	defer func() { close(release); workers.Wait() }()
	for range appAvailabilityConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			mux.ServeHTTP(availabilityBlockingWriter{httptest.NewRecorder(), started, release}, httptest.NewRequest("GET", "/app/demo/.shinyhub/availability", nil))
		}()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("response write failed to start")
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/.shinyhub/availability", nil))
	if rec.Code != 200 {
		t.Fatalf("slow client held lookup budget: %d %s", rec.Code, rec.Body)
	}
}

func TestAppAvailabilityDeadlineReturnsUnknownStateAndReleasesBudget(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	p := proxy.New()
	p.MarkSynced()
	var calls atomic.Int64
	var expired atomic.Int64
	store := availabilityStoreFunc(func(ctx context.Context, _ string) (*db.AppAvailability, error) {
		if calls.Add(1) <= appAvailabilityConcurrency {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > time.Second {
				return nil, errors.New("missing lookup deadline")
			}
			<-ctx.Done()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				expired.Add(1)
			}
			return nil, ctx.Err()
		}
		return &db.AppAvailability{Status: "hibernated", DeploymentID: 1}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("/app/{slug}/.shinyhub/availability", appAvailabilityHandler([]string{"demo"}, store, p, ready))
	// The parent bounds a regression that forgets the handler's own deadline.
	for i := range appAvailabilityConcurrency {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/.shinyhub/availability", nil).WithContext(ctx))
		parentErr := ctx.Err()
		cancel()
		if parentErr != nil || expired.Load() != int64(i+1) || rec.Code != 503 || rec.Body.String() != "{\"available\":false,\"status\":\"unknown\",\"reason\":\"state_unavailable\"}\n" {
			t.Fatalf("expired lookup: %d %s parent=%v expirations=%d", rec.Code, rec.Body, parentErr, expired.Load())
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/.shinyhub/availability", nil))
	if rec.Code != 200 {
		t.Fatalf("lookup did not recover after expiry: %d %s", rec.Code, rec.Body)
	}
}
