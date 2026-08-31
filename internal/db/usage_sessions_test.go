package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestAppUsageReportAggregatesSessionsAndProtectsIdentityDetail(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "usage-owner", "developer")
	viewer := mustCreateUser(t, store, "usage-viewer", "viewer")
	app := mustCreateApp(t, store, "usage-report", owner.ID)
	now := time.Now().UTC().Truncate(time.Second)

	starts := []db.UsageSessionStart{
		{ID: "known-a", Slug: app.Slug, UserID: viewer.ID, InstanceID: "cp-a", StartedAt: now.Add(-48*time.Hour - 5*time.Minute)},
		{ID: "known-b", Slug: app.Slug, UserID: viewer.ID, InstanceID: "cp-a", StartedAt: now.Add(-24*time.Hour - 2*time.Minute)},
		{ID: "anonymous-live", Slug: app.Slug, InstanceID: "cp-b", StartedAt: now.Add(-time.Minute)},
		{ID: "outside-window", Slug: app.Slug, UserID: viewer.ID, InstanceID: "cp-a", StartedAt: now.Add(-40 * 24 * time.Hour)},
	}
	for _, start := range starts {
		if err := store.BeginUsageSession(start); err != nil {
			t.Fatalf("begin %s: %v", start.ID, err)
		}
	}
	updates := []struct {
		id        string
		heartbeat time.Time
		ended     any
	}{
		{"known-a", starts[0].StartedAt.Add(5 * time.Minute), starts[0].StartedAt.Add(5 * time.Minute)},
		{"known-b", starts[1].StartedAt.Add(2 * time.Minute), starts[1].StartedAt.Add(2 * time.Minute)},
		{"anonymous-live", now, nil},
		{"outside-window", starts[3].StartedAt.Add(time.Minute), starts[3].StartedAt.Add(time.Minute)},
	}
	for _, update := range updates {
		if _, err := store.DB().Exec(`UPDATE usage_sessions SET heartbeat_at = ?, ended_at = ? WHERE id = ?`,
			update.heartbeat, update.ended, update.id); err != nil {
			t.Fatalf("update %s: %v", update.id, err)
		}
	}

	aggregate, err := store.AppUsageReport(context.Background(), app.ID, 30*24*time.Hour, "identified", false)
	if err != nil {
		t.Fatalf("aggregate report: %v", err)
	}
	if aggregate.Summary.Sessions != 3 || aggregate.Summary.UniqueViewers == nil || *aggregate.Summary.UniqueViewers != 1 ||
		aggregate.Summary.AuthenticatedSessions != 2 || aggregate.Summary.AnonymousSessions != 1 ||
		aggregate.Summary.ActiveSessions != 1 {
		t.Fatalf("summary = %+v", aggregate.Summary)
	}
	if aggregate.Summary.TotalDurationSeconds != 480 || aggregate.Summary.AverageDurationSeconds != 160 {
		t.Fatalf("durations = total %d average %v", aggregate.Summary.TotalDurationSeconds, aggregate.Summary.AverageDurationSeconds)
	}
	if len(aggregate.Daily) != 3 {
		t.Fatalf("daily buckets = %d, want 3", len(aggregate.Daily))
	}
	if len(aggregate.Viewers) != 0 || len(aggregate.Recent) != 0 {
		t.Fatal("aggregate report leaked identity detail")
	}

	detailed, err := store.AppUsageReport(context.Background(), app.ID, 30*24*time.Hour, "identified", true)
	if err != nil {
		t.Fatalf("detailed report: %v", err)
	}
	if len(detailed.Viewers) != 1 || detailed.Viewers[0].UserID != viewer.ID || detailed.Viewers[0].Sessions != 2 {
		t.Fatalf("viewers = %+v", detailed.Viewers)
	}
	if len(detailed.Recent) != 3 || !detailed.Recent[0].Active {
		t.Fatalf("recent = %+v", detailed.Recent)
	}
}

func TestUsageSessionRetentionAndForeignKeyPrivacy(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "retention-owner", "developer")
	viewer := mustCreateUser(t, store, "retention-viewer", "viewer")
	app := mustCreateApp(t, store, "usage-retention", owner.ID)
	now := time.Now().UTC().Truncate(time.Second)

	for _, start := range []db.UsageSessionStart{
		{ID: "fresh", Slug: app.Slug, UserID: viewer.ID, InstanceID: "cp", StartedAt: now.Add(-24 * time.Hour)},
		{ID: "expired", Slug: app.Slug, UserID: viewer.ID, InstanceID: "cp", StartedAt: now.Add(-100 * 24 * time.Hour)},
		{ID: "stale-open", Slug: app.Slug, UserID: viewer.ID, InstanceID: "cp", StartedAt: now.Add(-100 * 24 * time.Hour)},
		{ID: "live-old", Slug: app.Slug, UserID: viewer.ID, InstanceID: "cp", StartedAt: now.Add(-100 * 24 * time.Hour)},
	} {
		if err := store.BeginUsageSession(start); err != nil {
			t.Fatalf("begin %s: %v", start.ID, err)
		}
	}
	if err := store.EndUsageSession("expired", now.Add(-100*24*time.Hour+time.Minute)); err != nil {
		t.Fatalf("end expired session: %v", err)
	}
	if err := store.HeartbeatUsageSessions([]string{"live-old"}); err != nil {
		t.Fatalf("heartbeat live old session: %v", err)
	}
	removed, err := store.PruneUsageSessions(90 * 24 * time.Hour)
	if err != nil || removed != 2 {
		t.Fatalf("prune removed=%d err=%v", removed, err)
	}
	var rolledSessions, rolledPeople int64
	if err := store.DB().QueryRow(`SELECT sessions, person_sessions FROM usage_daily WHERE app_id = ?`, app.ID).Scan(&rolledSessions, &rolledPeople); err != nil {
		t.Fatalf("read usage rollup: %v", err)
	}
	if rolledSessions != 2 || rolledPeople != 2 {
		t.Fatalf("usage rollup sessions=%d people=%d", rolledSessions, rolledPeople)
	}
	var liveOldRows int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM usage_sessions WHERE id = 'live-old' AND ended_at IS NULL`).Scan(&liveOldRows); err != nil {
		t.Fatalf("read live old session: %v", err)
	}
	if liveOldRows != 1 {
		t.Fatal("freshly heartbeating old session was pruned")
	}
	if err := store.DeleteUser(viewer.ID); err != nil {
		t.Fatalf("delete viewer: %v", err)
	}
	var userID any
	if err := store.DB().QueryRow(`SELECT user_id FROM usage_sessions WHERE id = 'fresh'`).Scan(&userID); err != nil {
		t.Fatalf("read retained session: %v", err)
	}
	if userID != nil {
		t.Fatalf("deleted viewer identity retained as %v", userID)
	}
}

func TestUsageSessionInsertClampsCapturedAndCommittedPolicies(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "policy-owner", "developer")
	viewer := mustCreateUser(t, store, "policy-viewer", "viewer")
	app := mustCreateApp(t, store, "policy-clamp", owner.ID)
	if _, err := store.EnsureUsagePolicy("identified", []byte("encrypted-test-key")); err != nil {
		t.Fatal(err)
	}

	setOverride := func(mode string) {
		t.Helper()
		var value *string
		if mode != "inherit" {
			value = &mode
		}
		if _, _, _, _, _, err := store.PatchAppSettings(db.PatchAppSettingsParams{
			Slug: app.Slug, SetUsageIdentityMode: true, UsageIdentityMode: value,
		}); err != nil {
			t.Fatalf("set override %s: %v", mode, err)
		}
	}
	insert := func(id, capturedMode string) bool {
		t.Helper()
		stored, err := store.BeginUsageSessionWithPolicy(db.UsageSessionStart{
			ID: id, Slug: app.Slug, UserID: viewer.ID, ViewerKey: "app-scoped-key",
			PrincipalKind: "person", IdentityMode: capturedMode,
			PolicyGeneration: 1, InstanceID: "cp", StartedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		return stored
	}

	setOverride("pseudonymous")
	if !insert("downgrade-in-flight", "identified") {
		t.Fatal("pseudonymous policy unexpectedly disabled collection")
	}
	var mode string
	var userID, viewerKey any
	if err := store.DB().QueryRow(`SELECT identity_mode, user_id, viewer_key FROM usage_sessions WHERE id = ?`,
		"downgrade-in-flight").Scan(&mode, &userID, &viewerKey); err != nil {
		t.Fatal(err)
	}
	if mode != "pseudonymous" || userID != nil || viewerKey != "app-scoped-key" {
		t.Fatalf("downgrade clamp stored mode=%s user=%v viewer=%v", mode, userID, viewerKey)
	}

	setOverride("identified")
	if !insert("queued-before-upgrade", "unattributed") {
		t.Fatal("identified policy unexpectedly disabled collection")
	}
	if err := store.DB().QueryRow(`SELECT identity_mode, user_id, viewer_key FROM usage_sessions WHERE id = ?`,
		"queued-before-upgrade").Scan(&mode, &userID, &viewerKey); err != nil {
		t.Fatal(err)
	}
	if mode != "unattributed" || userID != nil || viewerKey != nil {
		t.Fatalf("prospective upgrade stored mode=%s user=%v viewer=%v", mode, userID, viewerKey)
	}

	setOverride("disabled")
	if insert("disabled-after-open", "identified") {
		t.Fatal("disabled committed policy accepted a captured identified session")
	}
}

func TestAppUsageReportUsesCompleteUTCCalendarDays(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "calendar-owner", "developer")
	app := mustCreateApp(t, store, "usage-calendar", owner.ID)
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	earliest := today.AddDate(0, 0, -6)

	for _, start := range []db.UsageSessionStart{
		{ID: "first-visible-day", Slug: app.Slug, InstanceID: "cp", StartedAt: earliest},
		{ID: "previous-day", Slug: app.Slug, InstanceID: "cp", StartedAt: earliest.Add(-time.Second)},
	} {
		if err := store.BeginUsageSession(start); err != nil {
			t.Fatalf("begin %s: %v", start.ID, err)
		}
	}

	report, err := store.AppUsageReport(context.Background(), app.ID, 7*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.Sessions != 1 || len(report.Daily) != 1 || report.Daily[0].Date != earliest.Format("2006-01-02") {
		t.Fatalf("calendar-day report = summary %+v daily %+v", report.Summary, report.Daily)
	}
}

func TestAppUsageReportCalculatesPeakConcurrentSessions(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "peak-owner", "developer")
	app := mustCreateApp(t, store, "usage-peak", owner.ID)
	now := time.Now().UTC().Truncate(time.Second)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if now.Sub(day) < 3*time.Hour {
		day = day.AddDate(0, 0, -1)
	}

	starts := []db.UsageSessionStart{
		{ID: "peak-a", Slug: app.Slug, InstanceID: "cp", StartedAt: day.Add(time.Hour)},
		{ID: "peak-b", Slug: app.Slug, InstanceID: "cp", StartedAt: day.Add(90 * time.Minute)},
		{ID: "peak-c", Slug: app.Slug, InstanceID: "cp", StartedAt: day.Add(2 * time.Hour)},
	}
	ends := []time.Time{
		day.Add(2 * time.Hour),
		day.Add(150 * time.Minute),
		day.Add(3 * time.Hour),
	}
	for i, start := range starts {
		if err := store.BeginUsageSession(start); err != nil {
			t.Fatal(err)
		}
		if err := store.EndUsageSession(start.ID, ends[i]); err != nil {
			t.Fatal(err)
		}
	}

	report, err := store.AppUsageReport(context.Background(), app.ID, 7*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.PeakConcurrentSessions != 2 {
		t.Fatalf("summary peak = %d, want 2", report.Summary.PeakConcurrentSessions)
	}
	if len(report.Daily) != 1 || report.Daily[0].PeakConcurrentSessions != 2 {
		t.Fatalf("daily peak = %+v, want 2", report.Daily)
	}
}

func TestAppUsageReportStreamsPeakAcrossUTCDayBoundaries(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "peak-boundary-owner", "developer")
	app := mustCreateApp(t, store, "usage-peak-boundary", owner.ID)
	now := time.Now().UTC().Truncate(time.Second)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -2)

	intervals := []struct {
		id         string
		start, end time.Time
	}{
		{"spans-midnight", day.Add(23 * time.Hour), day.Add(26 * time.Hour)},
		{"overlaps-next-day", day.Add(24*time.Hour + 30*time.Minute), day.Add(25 * time.Hour)},
		// Half-open semantics: this starts exactly when spans-midnight ends.
		{"starts-on-close", day.Add(26 * time.Hour), day.Add(27 * time.Hour)},
	}
	for _, interval := range intervals {
		if err := store.BeginUsageSession(db.UsageSessionStart{
			ID: interval.id, Slug: app.Slug, InstanceID: "cp", StartedAt: interval.start,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.EndUsageSession(interval.id, interval.end); err != nil {
			t.Fatal(err)
		}
	}

	report, err := store.AppUsageReport(context.Background(), app.ID, 7*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.PeakConcurrentSessions != 2 {
		t.Fatalf("summary peak = %d, want 2", report.Summary.PeakConcurrentSessions)
	}
	want := map[string]int64{
		day.Format("2006-01-02"):                  1,
		day.AddDate(0, 0, 1).Format("2006-01-02"): 2,
	}
	for _, got := range report.Daily {
		if peak, ok := want[got.Date]; ok {
			if got.PeakConcurrentSessions != peak {
				t.Fatalf("day %s peak = %d, want %d", got.Date, got.PeakConcurrentSessions, peak)
			}
			delete(want, got.Date)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing daily peak buckets: %v", want)
	}
}

func TestAppUsageReportHonorsCancellation(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "usage-cancel-owner", "developer")
	app := mustCreateApp(t, store, "usage-cancel", owner.ID)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.AppUsageReport(ctx, app.ID, 30*24*time.Hour, "unattributed", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("report error = %v, want context cancellation", err)
	}
}

func TestUsageConcurrencyQueryAvoidsTemporarySort(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	store := mustOpenDB(t)
	rows, err := store.DB().Query(`EXPLAIN QUERY PLAN
		SELECT started_at, ended_at, heartbeat_at
		FROM usage_sessions WHERE app_id = ? AND started_at < ?
		AND COALESCE(ended_at, heartbeat_at) > ?
		ORDER BY started_at, id`, 1, time.Now().UTC(), time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	plan := strings.Join(details, "\n")
	if strings.Contains(plan, "TEMP B-TREE") || !strings.Contains(plan, "idx_usage_sessions_app_started") {
		t.Fatalf("usage concurrency query plan is not index-ordered:\n%s", plan)
	}
}

func TestUsageRollupPreservesPeakConcurrentSessions(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "peak-rollup-owner", "developer")
	app := mustCreateApp(t, store, "usage-peak-rollup", owner.ID)
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-100 * 24 * time.Hour)

	for i, interval := range []struct{ offset, duration time.Duration }{
		{0, 2 * time.Hour},
		{30 * time.Minute, time.Hour},
	} {
		id := fmt.Sprintf("rolled-peak-%d", i)
		opened := start.Add(interval.offset)
		if err := store.BeginUsageSession(db.UsageSessionStart{
			ID: id, Slug: app.Slug, InstanceID: "cp", StartedAt: opened,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.EndUsageSession(id, opened.Add(interval.duration)); err != nil {
			t.Fatal(err)
		}
	}
	if removed, err := store.PruneUsageSessions(30 * 24 * time.Hour); err != nil || removed != 2 {
		t.Fatalf("prune removed=%d err=%v", removed, err)
	}
	var peakFinalized bool
	if err := store.DB().QueryRow(`SELECT peak_finalized FROM usage_daily WHERE app_id = ?`, app.ID).Scan(&peakFinalized); err != nil {
		t.Fatal(err)
	}
	if !peakFinalized {
		t.Fatal("rolled daily peak was not marked final")
	}
	report, err := store.AppUsageReport(context.Background(), app.ID, 365*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.PeakConcurrentSessions != 2 || len(report.Daily) != 1 || report.Daily[0].PeakConcurrentSessions != 2 {
		t.Fatalf("rolled peak report = summary %+v daily %+v", report.Summary, report.Daily)
	}
}

func TestUsageRollupLeavesCurrentUTCDayPeakMutable(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "peak-mutable-owner", "developer")
	app := mustCreateApp(t, store, "usage-peak-mutable", owner.ID)
	now := time.Now().UTC().Truncate(time.Second)
	start := now.Add(-31 * 24 * time.Hour)
	if err := store.BeginUsageSession(db.UsageSessionStart{
		ID: "long-running", Slug: app.Slug, InstanceID: "cp", StartedAt: start,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.EndUsageSession("long-running", now); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.PruneUsageSessions(30 * 24 * time.Hour); err != nil || removed != 1 {
		t.Fatalf("prune removed=%d err=%v", removed, err)
	}
	var finalized bool
	if err := store.DB().QueryRow(`SELECT peak_finalized FROM usage_daily WHERE app_id = ? AND day = ?`,
		app.ID, now.Format("2006-01-02")).Scan(&finalized); err != nil {
		t.Fatal(err)
	}
	if finalized {
		t.Fatal("current UTC day was finalized before later connections could raise its peak")
	}
}

func TestAppUsageReportWithholdsUniqueViewersAcrossIdentityTransitions(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "transition-owner", "developer")
	viewer := mustCreateUser(t, store, "transition-viewer", "viewer")
	now := time.Now().UTC().Truncate(time.Second)

	for _, fixture := range []struct {
		slug  string
		first db.UsageSessionStart
	}{
		{
			slug: "pseudo-to-identified",
			first: db.UsageSessionStart{ID: "pseudo-before-upgrade", ViewerKey: "same-person-app-key",
				PrincipalKind: "person", IdentityMode: "pseudonymous"},
		},
		{
			slug: "unattributed-to-identified",
			first: db.UsageSessionStart{ID: "unattributed-before-upgrade",
				PrincipalKind: "person", IdentityMode: "unattributed"},
		},
	} {
		app := mustCreateApp(t, store, fixture.slug, owner.ID)
		fixture.first.Slug = app.Slug
		fixture.first.InstanceID = "cp"
		fixture.first.StartedAt = now.Add(-time.Minute)
		if err := store.BeginUsageSession(fixture.first); err != nil {
			t.Fatal(err)
		}
		if err := store.BeginUsageSession(db.UsageSessionStart{
			ID: "identified-after-upgrade-" + fixture.slug, Slug: app.Slug, UserID: viewer.ID,
			PrincipalKind: "person", IdentityMode: "identified", InstanceID: "cp", StartedAt: now,
		}); err != nil {
			t.Fatal(err)
		}

		report, err := store.AppUsageReport(context.Background(), app.ID, 30*24*time.Hour, "identified", false)
		if err != nil {
			t.Fatal(err)
		}
		if report.Summary.AuthenticatedSessions != 2 || report.Summary.UniqueViewers != nil {
			t.Fatalf("%s summary claimed exact uniqueness: %+v", fixture.slug, report.Summary)
		}
		if len(report.Daily) != 1 || report.Daily[0].UniqueViewers != nil {
			t.Fatalf("%s daily claimed exact uniqueness: %+v", fixture.slug, report.Daily)
		}
	}
}
