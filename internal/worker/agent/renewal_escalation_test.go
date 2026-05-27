package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// captureHandler records the slog records emitted through it so a test can assert
// the level and attributes of renewal logs.
type captureHandler struct {
	mu   *sync.Mutex
	recs *[]slog.Record
}

func (c captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.recs = append(*c.recs, r.Clone())
	return nil
}
func (c captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c captureHandler) WithGroup(string) slog.Handler      { return c }

// attrInt returns the int value of the named attribute on r, or false if absent.
func attrInt(r slog.Record, key string) (int64, bool) {
	var v int64
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, found = a.Value.Int64(), true
			return false
		}
		return true
	})
	return v, found
}

// TestRecordRenewal_EscalatesAndTracksStreak verifies that recording renewal
// outcomes escalates the log level as the cert nears expiry, threads the
// consecutive-failure count through the log, resets that streak on a successful
// renewal, and stays silent when there is nothing to renew.
func TestRecordRenewal_EscalatesAndTracksStreak(t *testing.T) {
	var mu sync.Mutex
	var recs []slog.Record
	prev := slog.Default()
	slog.SetDefault(slog.New(captureHandler{mu: &mu, recs: &recs}))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a := &Agent{nodeID: "node-x"}
	ctx := context.Background()
	na := time.Now().Add(time.Minute)

	a.recordRenewal(ctx, renewalDue, na, false, errors.New("cp down"))
	a.recordRenewal(ctx, renewalCritical, na, false, errors.New("cp down"))
	a.recordRenewal(ctx, renewalDue, na, true, nil)
	a.recordRenewal(ctx, renewalFresh, time.Time{}, false, nil)

	if a.renewFailures != 0 {
		t.Errorf("renewFailures = %d, want 0 after a successful renewal", a.renewFailures)
	}
	if len(recs) != 3 {
		t.Fatalf("emitted %d log records, want 3 (warn, error, info; fresh is silent)", len(recs))
	}
	if recs[0].Level != slog.LevelWarn {
		t.Errorf("first record level = %v, want warn", recs[0].Level)
	}
	if got, _ := attrInt(recs[0], "consecutive_failures"); got != 1 {
		t.Errorf("first record consecutive_failures = %d, want 1", got)
	}
	if recs[1].Level != slog.LevelError {
		t.Errorf("second record level = %v, want error", recs[1].Level)
	}
	if got, _ := attrInt(recs[1], "consecutive_failures"); got != 2 {
		t.Errorf("second record consecutive_failures = %d, want 2", got)
	}
	if recs[2].Level != slog.LevelInfo {
		t.Errorf("third record level = %v, want info", recs[2].Level)
	}
	if _, ok := attrInt(recs[2], "consecutive_failures"); ok {
		t.Error("renewed (info) record should not carry consecutive_failures")
	}
}

// TestClassifyRenewal_Boundaries pins the lifetime phases that drive renewal: the
// agent requests renewal from the half-life onward, and an unfulfilled request is
// treated as critical once the cert is past 90% of its lifetime (only the final
// 10% of runway remains before the worker loses its routing identity).
func TestClassifyRenewal_Boundaries(t *testing.T) {
	nb := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	na := nb.Add(time.Hour) // due at +30m, critical at +54m

	cases := []struct {
		name string
		now  time.Time
		want renewalPhase
	}{
		{"fresh", nb.Add(time.Minute), renewalFresh},
		{"just before half-life", nb.Add(29 * time.Minute), renewalFresh},
		{"at half-life", nb.Add(30 * time.Minute), renewalDue},
		{"past half-life", nb.Add(45 * time.Minute), renewalDue},
		{"just before 90%", nb.Add(53 * time.Minute), renewalDue},
		{"at 90%", nb.Add(54 * time.Minute), renewalCritical},
		{"expired", na.Add(time.Minute), renewalCritical},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRenewal(nb, na, tc.now); got != tc.want {
				t.Errorf("classifyRenewal(now=%s) = %v, want %v", tc.now.Sub(nb), got, tc.want)
			}
		})
	}
}

// TestRenewalLogFor selects the log severity for a renewal-relevant heartbeat: a
// successful swap is info, a pending renewal escalates from warn to error as the
// cert nears expiry, and a fresh cert needing nothing is silent.
func TestRenewalLogFor(t *testing.T) {
	cases := []struct {
		name      string
		phase     renewalPhase
		renewed   bool
		wantLog   bool
		wantLevel slog.Level
	}{
		{"fresh and unrenewed is silent", renewalFresh, false, false, 0},
		{"due and pending warns", renewalDue, false, true, slog.LevelWarn},
		{"critical and pending errors", renewalCritical, false, true, slog.LevelError},
		{"renewed logs info", renewalDue, true, true, slog.LevelInfo},
		{"renewed from critical still info", renewalCritical, true, true, slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			level, _, log := renewalLogFor(tc.phase, tc.renewed)
			if log != tc.wantLog {
				t.Fatalf("renewalLogFor(%v, renewed=%v) log = %v, want %v", tc.phase, tc.renewed, log, tc.wantLog)
			}
			if log && level != tc.wantLevel {
				t.Errorf("renewalLogFor(%v, renewed=%v) level = %v, want %v", tc.phase, tc.renewed, level, tc.wantLevel)
			}
		})
	}
}
