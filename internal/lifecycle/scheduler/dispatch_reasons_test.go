package scheduler

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"testing"

	"github.com/rvben/shinyhub/internal/metrics"
)

// The registry seeds dispatch-failure reasons from its own list, because the
// metrics package cannot import this one. A reason added here and not there
// would publish no zero series, so its first occurrence could never alert.
func TestDispatchRejectReasons_AreTheReasonsTheRegistrySeeds(t *testing.T) {
	reg := metrics.New("test")
	reg.SeedSchedule("pin", "pin")
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	re := regexp.MustCompile(`shinyhub_schedule_dispatch_failures_total\{[^}]*reason="([a-z_]+)"[^}]*\} 0`)
	var seeded []string
	for _, m := range re.FindAllStringSubmatch(rec.Body.String(), -1) {
		seeded = append(seeded, m[1])
	}
	want := append([]string(nil), DispatchRejectReasons...)
	sort.Strings(seeded)
	sort.Strings(want)
	if len(seeded) == 0 || len(seeded) != len(want) {
		t.Fatalf("registry seeds reasons %v, scheduler can record %v", seeded, want)
	}
	for i := range want {
		if seeded[i] != want[i] {
			t.Fatalf("registry seeds reasons %v, scheduler can record %v", seeded, want)
		}
	}
}
