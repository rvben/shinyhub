package api

import (
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/history"
)

func TestDecorateCPUCapacity(t *testing.T) {
	h := history.NewStore(time.Hour, 15*time.Second)
	s := &Server{hostCapacity: &HostCapacity{Cores: 16, CoresSource: "affinity"}, history: h}
	a, b := 95.0, 405.0
	resp := &metricsResponse{Replicas: []replicaMetrics{
		{Index: 0, Status: "running", PID: 10, RunID: "run-a", MetricsAvailable: true, CPUPercent: &a, CPUQuotaEnforced: true, EffectiveCPUQuotaPercent: 100},
		{Index: 1, Status: "running", PID: 11, RunID: "run-b", MetricsAvailable: true, CPUPercent: &b, CPUQuotaEnforced: true, EffectiveCPUQuotaPercent: 500},
	}}
	s.decorateCPUCapacity("app", resp)
	if !resp.CPUSaturationAvailable {
		t.Fatal("wired history collector must advertise saturation availability")
	}
	if resp.CPUCores == nil || *resp.CPUCores != 5 || resp.CPUCapacityCores == nil || *resp.CPUCapacityCores != 6 || resp.CPUCapacitySource != "quota" {
		t.Fatalf("CPU usage/capacity = %v/%v (%s), want 5/6 (quota)", resp.CPUCores, resp.CPUCapacityCores, resp.CPUCapacitySource)
	}
	if resp.CPUHostCores == nil || *resp.CPUHostCores != 16 {
		t.Fatalf("host cores = %v, want 16", resp.CPUHostCores)
	}
	if resp.Replicas[0].CPUSaturated {
		t.Fatal("first high sample must not claim sustained saturation")
	}
	now := time.Now().Unix()
	for _, at := range []int64{now - 30, now - 15, now} {
		h.RecordReplicaCPU("app", 0, "run-a", at, &a)
		h.RecordReplicaCPU("app", 1, "run-b", at, &b)
	}
	s.decorateCPUCapacity("app", resp)
	if !resp.Replicas[0].CPUSaturated || resp.Replicas[1].CPUSaturated {
		t.Fatal("only the replica above 90% for three samples should be saturated")
	}
	a = 20
	s.decorateCPUCapacity("app", resp)
	if resp.Replicas[0].CPUSaturated {
		t.Fatal("a low live rate must clear a historic hot warning")
	}
	resp.Replicas[0].CPUPercent = nil
	s.decorateCPUCapacity("app", resp)
	if resp.CPUCores != nil || resp.Replicas[0].CPUSaturated {
		t.Fatal("a missing rate must clear the total and saturation")
	}
}

func TestEffectiveAutoscaleMaxReplicas(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runtimeMax int
		appMax     int
		want       int
	}{
		{"app ceiling", 16, 8, 8},
		{"runtime lowered after policy saved", 4, 8, 4},
		{"runtime fallback", 0, 64, 32},
		{"no app maximum", 16, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{Runtime: config.RuntimeConfig{MaxReplicas: tc.runtimeMax}}}
			app := &db.App{AutoscaleMaxReplicas: tc.appMax}
			if got := s.effectiveAutoscaleMaxReplicas(app); got != tc.want {
				t.Fatalf("effective maximum = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMetricsWorkerIsolationBeforePoolExists(t *testing.T) {
	for _, mode := range []string{"grouped", "per_session"} {
		t.Run(mode, func(t *testing.T) {
			s := &Server{cfg: &config.Config{Runtime: config.RuntimeConfig{DefaultWorkerIsolation: mode}}}
			app := &db.App{Slug: "app", Status: "stopped", WorkerMaxWorkers: 12, AutoscaleMaxReplicas: 8}
			resp := s.buildAppMetricsFrom("app", app, nil, db.AuditEvent{}, false)
			if resp.WorkerIsolation != mode || resp.MaxWorkers != 12 {
				t.Fatalf("isolation/max workers = %q/%d, want %s/12", resp.WorkerIsolation, resp.MaxWorkers, mode)
			}
			if resp.EffectiveAutoscaleMaxReplicas != 0 {
				t.Fatal("elastic workers must not advertise a replica autoscale maximum")
			}
		})
	}
}

func TestSumAutoscaleSessionCounts(t *testing.T) {
	if sumAutoscaleSessionCounts(nil) != nil {
		t.Fatal("missing pool must not look like zero active sessions")
	}
	got := sumAutoscaleSessionCounts([]int64{30, -1, 12})
	if got == nil || *got != 42 {
		t.Fatalf("active sessions = %v, want 42", got)
	}
}

func TestDecorateCPUCapacityFallsBackToHost(t *testing.T) {
	s := &Server{hostCapacity: &HostCapacity{Cores: 4, CoresSource: "affinity"}}
	cpu := 100.0
	resp := &metricsResponse{Replicas: []replicaMetrics{{Index: 0, Status: "running", PID: 10,
		MetricsAvailable: true, CPUPercent: &cpu, EffectiveCPUQuotaPercent: 50}}}
	s.decorateCPUCapacity("app", resp)
	if resp.CPUSaturationAvailable {
		t.Fatal("disabled history must report saturation unavailable")
	}
	if resp.CPUCapacityCores == nil || *resp.CPUCapacityCores != 4 || resp.CPUCapacitySource != "affinity" {
		t.Fatalf("capacity = %v (%s), want 4 (affinity)", resp.CPUCapacityCores, resp.CPUCapacitySource)
	}
}

func TestDecorateCPUCapacityNeverExceedsHost(t *testing.T) {
	s := &Server{hostCapacity: &HostCapacity{Cores: 4, CoresSource: "affinity"}}
	cpu := 100.0
	resp := &metricsResponse{Replicas: []replicaMetrics{
		{Index: 0, Status: "running", MetricsAvailable: true, CPUPercent: &cpu, CPUQuotaEnforced: true, EffectiveCPUQuotaPercent: 300},
		{Index: 1, Status: "running", MetricsAvailable: true, CPUPercent: &cpu, CPUQuotaEnforced: true, EffectiveCPUQuotaPercent: 300},
	}}
	s.decorateCPUCapacity("app", resp)
	if resp.CPUCapacityCores == nil || *resp.CPUCapacityCores != 4 || resp.CPUCapacitySource != "affinity" {
		t.Fatalf("capacity = %v (%s), want host-bound 4 cores", resp.CPUCapacityCores, resp.CPUCapacitySource)
	}
}
