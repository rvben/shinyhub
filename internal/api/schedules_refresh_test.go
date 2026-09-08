package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/jobs"
)

func TestScheduleRefreshStaleFreshDisabledAndCrossApp(t *testing.T) {
	srv, store, token := newScheduleE2EServerWithJobs(t)
	for _, slug := range []string{"refresh-app", "other-app"} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: 1, Access: "private"}); err != nil {
			t.Fatal(err)
		}
	}
	app, _ := store.GetAppBySlug("refresh-app")
	for _, enabled := range []bool{true, false} {
		name := fmt.Sprintf("refresh-%t", enabled)
		id, err := store.CreateSchedule(db.CreateScheduleParams{AppID: app.ID, Name: name, CronExpr: "0 * * * *", CommandJSON: `["true"]`, Enabled: enabled, TimeoutSeconds: 30, OverlapPolicy: "concurrent", MissedPolicy: "skip"})
		if err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/api/apps/refresh-app/schedules/%d/refresh-stale", id)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, "POST", path, nil, token))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		var result jobs.RefreshResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		want := "fresh"
		if !enabled {
			want = "disabled"
		}
		if result.Status != want || result.ScheduleID != id || result.Schedule != name || result.RunID != 0 {
			t.Fatalf("result=%+v", result)
		}
		rec = httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, "POST", fmt.Sprintf("/api/apps/other-app/schedules/%d/refresh-stale", id), nil, token))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("cross-app status=%d body=%s", rec.Code, rec.Body)
		}
		rec = httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest("POST", path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous status=%d", rec.Code)
		}
	}
}

func TestScheduleRefreshStaleCancelledAppLock(t *testing.T) {
	srv, store, token := newScheduleE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "refresh-app", Name: "refresh", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	release, err := srv.AcquireAppOperation("refresh-app")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	req := authedRequest(t, "POST", "/api/apps/refresh-app/schedules/1/refresh-stale", nil, token).WithContext(ctx)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestTimeout {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}
