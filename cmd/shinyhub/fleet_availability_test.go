package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestFleetAvailabilityDynamicAnonymousNonWaking(t *testing.T) {
	for _, isolated := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "isolated"}[isolated], func(t *testing.T) {
			h := newTestHTTPHandler(t, func(c *config.Config) {
				c.Server.AppAvailabilityOptIn = "allow"
				c.Server.PublicFleetAvailability = true
				c.Server.PublicAppAvailability = []string{"central", "missing"}
				c.Auth.ForwardAuth.Enabled = true
				if isolated {
					c.Server.AppOrigin = "https://apps.example.com"
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
			for _, slug := range []string{"secret", "opted", "central"} {
				if _, err := h.store.CreateApp(db.CreateAppParams{Slug: slug, Name: "Sensitive app name", OwnerID: u.ID, Access: "private"}); err != nil {
					t.Fatal(err)
				}
				a, err := h.store.GetAppBySlug(slug)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.store.CreateDeployment(db.CreateDeploymentParams{AppID: a.ID, Version: "v1", BundleDir: t.TempDir()}); err != nil {
					t.Fatal(err)
				}
				if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "hibernated"}); err != nil {
					t.Fatal(err)
				}
			}
			var wakes atomic.Int64
			h.prx.SetWakeTrigger(func(context.Context, string) { wakes.Add(1) })
			probe := func(method, path string) *httptest.ResponseRecorder {
				t.Helper()
				rec := httptest.NewRecorder()
				h.handler.ServeHTTP(rec, httptest.NewRequest(method, "https://apps.example.com"+path, nil))
				return rec
			}
			check := func(want []string) fleetAvailabilityResponse {
				t.Helper()
				rec := probe("GET", "/.shinyhub/availability")
				var body fleetAvailabilityResponse
				if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.GeneratedAt.IsZero() || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Set-Cookie") != "" {
					t.Fatalf("fleet: %d %v %s", rec.Code, rec.Header(), rec.Body)
				}
				slugs := make([]string, 0)
				for _, a := range body.Apps {
					slugs = append(slugs, a.Slug)
				}
				if !reflect.DeepEqual(slugs, want) {
					t.Fatalf("slugs=%v want %v", slugs, want)
				}
				if strings.Contains(rec.Body.String(), "Sensitive") || strings.Contains(rec.Body.String(), "secret") {
					t.Fatalf("leaked private metadata: %s", rec.Body)
				}
				return body
			}
			check([]string{"central"})
			if rec := probe("GET", "/app/opted/.shinyhub/availability"); rec.Code != 404 {
				t.Fatalf("unlisted app: %d", rec.Code)
			}
			if _, err := h.store.PatchAppSettings(db.PatchAppSettingsParams{Slug: "opted", SetAvailability: true, Availability: "public"}); err != nil {
				t.Fatal(err)
			}
			before, err := h.store.GetAppBySlug("opted")
			if err != nil {
				t.Fatal(err)
			}
			idle := h.prx.IdleSince("opted")
			for range 5 {
				body := check([]string{"central", "opted"})
				if isolated {
					control := httptest.NewRecorder()
					h.handler.ServeHTTP(control, httptest.NewRequest("GET", "http://127.0.0.1/.shinyhub/availability", nil))
					var inventory fleetAvailabilityResponse
					if control.Code != 200 || json.Unmarshal(control.Body.Bytes(), &inventory) != nil || !reflect.DeepEqual(inventory.Apps, body.Apps) || control.Header().Get("Cache-Control") != "no-store" || control.Header().Get("Set-Cookie") != "" {
						t.Fatalf("control-origin inventory differs: %d %s", control.Code, control.Body)
					}
				}
				single := probe("GET", "/app/opted/.shinyhub/availability")
				var one appAvailabilityResponse
				if single.Code != 200 || json.Unmarshal(single.Body.Bytes(), &one) != nil || body.Apps[1].appAvailabilityResponse != one {
					t.Fatalf("per-app mismatch: %d %s %+v", single.Code, single.Body, body)
				}
			}
			after, err := h.store.GetAppBySlug("opted")
			if err != nil {
				t.Fatal(err)
			}
			if wakes.Load() != 0 || !reflect.DeepEqual(before, after) || !h.prx.IdleSince("opted").Equal(idle) || !h.prx.LastSeen("opted").IsZero() {
				t.Fatal("monitoring changed app state/activity")
			}
			if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "opted", Status: "stopped", OperatorStopped: true}); err != nil {
				t.Fatal(err)
			}
			body := check([]string{"central", "opted"})
			if body.Apps[1].Available || body.Apps[1].Reason != "stopped_by_operator" {
				t.Fatalf("stop reason: %+v", body.Apps[1])
			}
			if _, err := h.store.PatchAppSettings(db.PatchAppSettingsParams{Slug: "opted", SetAvailability: true, Availability: "off"}); err != nil {
				t.Fatal(err)
			}
			check([]string{"central"})
			if rec := probe("GET", "/app/opted/.shinyhub/availability"); rec.Code != 404 {
				t.Fatalf("revoked: %d", rec.Code)
			}
			if rec := probe("HEAD", "/.shinyhub/availability"); rec.Code != 200 || rec.Body.Len() != 0 {
				t.Fatalf("HEAD: %d %s", rec.Code, rec.Body)
			}
			if rec := probe("POST", "/.shinyhub/availability"); rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
				t.Fatalf("POST: %d %v", rec.Code, rec.Header())
			}
		})
	}
}

type fleetAvailabilityStoreStub struct {
	one  func(context.Context, string) (*db.AppAvailability, error)
	list func(context.Context, []string, bool) ([]db.AppAvailability, error)
}

func (s fleetAvailabilityStoreStub) GetAppAvailability(c context.Context, slug string) (*db.AppAvailability, error) {
	return s.one(c, slug)
}
func (s fleetAvailabilityStoreStub) ListAppAvailability(c context.Context, slugs []string, allow bool) ([]db.AppAvailability, error) {
	return s.list(c, slugs, allow)
}

func TestFleetAvailabilityErrorsAndSharedBudget(t *testing.T) {
	p := proxy.New()
	ready := make(chan struct{})
	close(ready)
	var listErr error
	var entries = []db.AppAvailability{}
	calls := 0
	source := fleetAvailabilityStoreStub{
		one: func(context.Context, string) (*db.AppAvailability, error) {
			t.Fatal("busy request queried DB")
			return nil, nil
		},
		list: func(ctx context.Context, slugs []string, allow bool) ([]db.AppAvailability, error) {
			calls++
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
				t.Fatal("unbounded snapshot")
			}
			return entries, listErr
		},
	}
	m := newAvailabilityMonitor([]string{"central"}, true, true, source, p, ready)
	mux := http.NewServeMux()
	mux.HandleFunc("/.shinyhub/availability", m.fleet)
	mux.HandleFunc("/app/{slug}/.shinyhub/availability", m.app)
	probe := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
		return rec
	}
	if rec := probe("GET", "/.shinyhub/availability"); rec.Code != 503 || !strings.Contains(rec.Body.String(), "platform_unavailable") {
		t.Fatalf("unsynced: %d %s", rec.Code, rec.Body)
	}
	p.MarkSynced()
	if rec := probe("GET", "/.shinyhub/availability"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"apps":[]`) {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
	for _, err := range []error{errors.New("secret database detail"), db.ErrAvailabilityFleetTooLarge, context.DeadlineExceeded} {
		listErr = err
		rec := probe("GET", "/.shinyhub/availability")
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "state_unavailable") || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), `"apps"`) {
			t.Fatalf("uncertain snapshot: %d %s", rec.Code, rec.Body)
		}
	}
	listErr = nil
	entries = []db.AppAvailability{{Slug: "central", Status: "crashed"}}
	if rec := probe("GET", "/.shinyhub/availability"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("unavailable app should not fail inventory: %d %s", rec.Code, rec.Body)
	}
	for range appAvailabilityConcurrency {
		m.lookups <- struct{}{}
	}
	before := calls
	for _, path := range []string{"/.shinyhub/availability", "/app/central/.shinyhub/availability"} {
		if rec := probe("GET", path); rec.Code != 429 || rec.Header().Get("Retry-After") != "1" {
			t.Fatalf("shared budget: %d %s", rec.Code, rec.Body)
		}
	}
	if calls != before {
		t.Fatal("busy request queried DB")
	}
	for range appAvailabilityConcurrency {
		<-m.lookups
	}
	m.fleetEnabled = false
	if rec := probe("GET", "/.shinyhub/availability"); rec.Code != 404 {
		t.Fatalf("disabled discovery: %d", rec.Code)
	}
	if calls != before {
		t.Fatal("disabled discovery queried DB")
	}
}

func TestAvailabilityStopReasonRequiresIntentAndPreservesQuarantine(t *testing.T) {
	for _, tc := range []struct {
		a      db.AppAvailability
		reason string
	}{
		{db.AppAvailability{Status: "stopped"}, ""},
		{db.AppAvailability{Status: "stopped", OperatorStopped: true}, "stopped_by_operator"},
		{db.AppAvailability{Status: "stopped", OperatorStopped: true, ServingBlocked: true}, "compatibility_blocked"},
		{db.AppAvailability{Status: "failed", OperatorStopped: true}, ""},
		{db.AppAvailability{Status: "deleting", OperatorStopped: true}, ""},
	} {
		got := evaluateAppAvailability(tc.a, true, true)
		if got.Available || got.Reason != tc.reason {
			t.Fatalf("%+v: %+v", tc.a, got)
		}
	}
}

func TestAvailabilityOperatorPolicyAndCentralOverride(t *testing.T) {
	for _, allow := range []bool{false, true} {
		h := newTestHTTPHandler(t, func(c *config.Config) {
			if allow {
				c.Server.AppAvailabilityOptIn = "allow"
			}
			c.Server.PublicAppAvailability = []string{"central"}
			c.Server.PublicFleetAvailability = true
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
		for _, slug := range []string{"central", "opted"} {
			if _, err := h.store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: u.ID}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.store.PatchAppSettings(db.PatchAppSettingsParams{Slug: "opted", SetAvailability: true, Availability: "public"}); err != nil {
			t.Fatal(err)
		}
		if rec := h.get(t, "/app/central/.shinyhub/availability", nil); rec.Code != 503 {
			t.Fatalf("central override allow=%v: %d", allow, rec.Code)
		}
		want := 404
		if allow {
			want = 503
		}
		if rec := h.get(t, "/app/opted/.shinyhub/availability", nil); rec.Code != want {
			t.Fatalf("opt-in policy allow=%v: %d", allow, rec.Code)
		}
		rec := h.get(t, "/.shinyhub/availability", nil)
		var body fleetAvailabilityResponse
		if rec.Code != 200 || json.Unmarshal(rec.Body, &body) != nil {
			t.Fatalf("fleet: %d %s", rec.Code, rec.Body)
		}
		n := 1
		if allow {
			n = 2
		}
		if len(body.Apps) != n {
			t.Fatalf("fleet ignored operator policy: %+v", body)
		}
	}
}
