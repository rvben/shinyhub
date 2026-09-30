package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

type cpuCapacityErrorSampler struct {
	failedPID int
}

func (s *cpuCapacityErrorSampler) Sample(handle process.RunHandle) (process.Stats, error) {
	if handle.PID == s.failedPID {
		return process.Stats{}, errors.New("temporary stats read failure")
	}
	return process.Stats{CPUPercent: process.Float(100), RSSBytes: 100}, nil
}

// A stats failure must leave process counts intact and make the whole CPU
// total unavailable. Recovery must restore the total without a fake restart.
func TestCPUCapacityMixedSamplingFailureRemainsUnavailable(t *testing.T) {
	srv, store, mgr := newMetricsTestServer(t)
	slug, token := seedAutoscaleApp(t, store)
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppReplicas(app.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	for index, pid := range []int{11, 12} {
		mgr.ForceEntry(slug, process.ProcessInfo{Slug: slug, Index: index, PID: pid, Status: process.StatusRunning})
	}
	srv.SetHostCapacity(4, "affinity", 0, "")
	sampler := &cpuCapacityErrorSampler{failedPID: 12}
	srv.SetSampler(sampler)
	for _, recovered := range []bool{false, true} {
		if recovered {
			sampler.failedPID = 0
		}
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/apps/"+slug+"/metrics", nil, token))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Status          string   `json:"status"`
			Cores           *float64 `json:"cpu_cores"`
			Capacity        *float64 `json:"cpu_capacity_cores"`
			ReplicasRunning int      `json:"replicas_running"`
			Replicas        []struct {
				Status           string   `json:"status"`
				MetricsAvailable bool     `json:"metrics_available"`
				CPUPercent       *float64 `json:"cpu_percent"`
			} `json:"replicas"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != "running" || response.ReplicasRunning != 2 || len(response.Replicas) != 2 || response.Replicas[1].Status != "running" {
			t.Fatalf("sampling changed process health: %+v", response)
		}
		if response.Capacity == nil || *response.Capacity != 4 {
			t.Fatalf("capacity = %v, want unchanged 4 cores", response.Capacity)
		}
		if recovered {
			if response.Cores == nil || *response.Cores != 2 || !response.Replicas[1].MetricsAvailable {
				t.Fatalf("recovered total = %+v, want 2 cores and available metrics", response)
			}
		} else if response.Cores != nil || response.Replicas[1].CPUPercent != nil || response.Replicas[1].MetricsAvailable {
			t.Fatalf("mixed failure published a partial total: %+v", response)
		}
	}
}
