package schedulespec

import (
	"testing"
	"time"
)

func ptr(t time.Time) *time.Time { return &t }

func TestIsStale(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	daily := "0 6 * * *" // every day at 06:00 UTC

	cases := []struct {
		name string
		f    Freshness
		want bool
	}{
		{
			name: "fresh: succeeded today at 06:00, now noon",
			f:    Freshness{Enabled: true, CronExpr: daily, TimeoutSeconds: 3600, LastSuccessAt: ptr(now.Add(-6 * time.Hour))},
			want: false, // next fire after 06:00 today is 06:00 tomorrow; not past
		},
		{
			name: "stale: last success yesterday 06:00, today's 06:00 + margin elapsed",
			f:    Freshness{Enabled: true, CronExpr: daily, TimeoutSeconds: 3600, LastSuccessAt: ptr(now.Add(-30 * time.Hour))},
			want: true, // next fire after yesterday 06:00 is today 06:00; +10m < noon
		},
		{
			name: "never succeeded, created seconds ago: not yet stale",
			f:    Freshness{Enabled: true, CronExpr: daily, TimeoutSeconds: 3600, CreatedAt: now.Add(-30 * time.Second)},
			want: false, // next fire after creation is tomorrow 06:00
		},
		{
			name: "never succeeded, created long ago: stale",
			f:    Freshness{Enabled: true, CronExpr: daily, TimeoutSeconds: 3600, CreatedAt: now.Add(-48 * time.Hour)},
			want: true,
		},
		{
			name: "disabled: never stale",
			f:    Freshness{Enabled: false, CronExpr: daily, TimeoutSeconds: 3600, CreatedAt: now.Add(-48 * time.Hour)},
			want: false,
		},
		{
			name: "running within timeout: data remains stale until the run succeeds",
			f: Freshness{
				Enabled: true, CronExpr: daily, TimeoutSeconds: 7200,
				LastSuccessAt: ptr(now.Add(-48 * time.Hour)),
				LastRunStatus: "running", LastRunAt: ptr(now.Add(-30 * time.Minute)),
			},
			want: true,
		},
		{
			name: "running but past its timeout (zombie): stale applies",
			f: Freshness{
				Enabled: true, CronExpr: daily, TimeoutSeconds: 600,
				LastSuccessAt: ptr(now.Add(-48 * time.Hour)),
				LastRunStatus: "running", LastRunAt: ptr(now.Add(-30 * time.Minute)),
			},
			want: true,
		},
		{
			name: "timeout NOT added to grace: daily with 24h timeout still stale at T+~24h",
			f: Freshness{
				Enabled: true, CronExpr: daily, TimeoutSeconds: 86400,
				LastSuccessAt: ptr(now.Add(-30 * time.Hour)), LastRunStatus: "failed",
			},
			want: true, // grace is 10m, not 24h+10m
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsStale(tc.f, loc, now); got != tc.want {
				t.Fatalf("IsStale = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsRefreshing(t *testing.T) {
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		f    Freshness
		want bool
	}{
		{
			name: "active run within timeout",
			f: Freshness{Enabled: true, TimeoutSeconds: 3600,
				LastRunStatus: "running", LastRunAt: ptr(now.Add(-5 * time.Minute))},
			want: true,
		},
		{
			name: "zombie run past timeout",
			f: Freshness{Enabled: true, TimeoutSeconds: 60,
				LastRunStatus: "running", LastRunAt: ptr(now.Add(-5 * time.Minute))},
		},
		{
			name: "disabled schedule",
			f: Freshness{Enabled: false, TimeoutSeconds: 3600,
				LastRunStatus: "running", LastRunAt: ptr(now.Add(-5 * time.Minute))},
		},
		{
			name: "terminal run",
			f: Freshness{Enabled: true, TimeoutSeconds: 3600,
				LastRunStatus: "failed", LastRunAt: ptr(now.Add(-5 * time.Minute))},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRefreshing(tc.f, now); got != tc.want {
				t.Fatalf("IsRefreshing = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsStale_WeeklyCadence(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC) // Tuesday
	weekly := "0 6 * * 1"                                // Mondays 06:00
	// Succeeded last Monday 06:00 (~1 day 6h ago). Next fire is next Monday; not stale.
	f := Freshness{Enabled: true, CronExpr: weekly, TimeoutSeconds: 3600, LastSuccessAt: ptr(now.Add(-30 * time.Hour))}
	if IsStale(f, loc, now) {
		t.Fatal("weekly schedule that ran this Monday should not be stale on Tuesday")
	}
}

func TestEvaluateStale_InvalidCronIsUnknown(t *testing.T) {
	_, err := EvaluateStale(Freshness{
		Enabled: true, CronExpr: "not a cron", CreatedAt: time.Now().Add(-24 * time.Hour),
	}, time.UTC, time.Now())
	if err == nil {
		t.Fatal("invalid cron returned a freshness value; want an error")
	}
}

func TestIsRefreshing_UsesActiveRunWhenLatestRunIsTerminal(t *testing.T) {
	now := time.Now()
	active := now.Add(-time.Minute)
	latest := now.Add(-30 * time.Second)
	f := Freshness{
		Enabled: true, TimeoutSeconds: 300,
		LastRunStatus: "skipped_overlap", LastRunAt: &latest, ActiveRunAt: &active,
	}
	if !IsRefreshing(f, now) {
		t.Fatal("an active run hidden by a newer overlap record must still be refreshing")
	}
}
