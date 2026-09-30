package db_test

import (
	"context"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestLiveSettingsRedeployRecordsOutcomeWithoutOwingReplacement(t *testing.T) {
	for _, outcome := range []string{db.RedeployCompleted, db.RedeployFailed, ""} {
		t.Run("outcome_"+outcome, func(t *testing.T) {
			s := dbtest.New(t)
			newRedeployApp(t, s, "live")
			patch, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "live", SetWorkerMaxWorkers: true, WorkerMaxWorkers: 4, ArmRedeploy: true})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := s.ResolveLiveSettingsRedeploy(context.Background(), nil, "live", patch, outcome, "live update")
			if err != nil || !resolved {
				t.Fatalf("resolve: %v %v", resolved, err)
			}
			full, err := s.RedeployNeedsFullCycle(context.Background(), "live")
			if err != nil || full {
				t.Fatalf("live update still owes replacement: %v %v", full, err)
			}
			app := mustGetApp(t, s, "live")
			if outcome == "" {
				owed, err := s.ListOwedRedeploys()
				if err != nil || len(owed) != 1 || app.LastRedeploy != nil {
					t.Fatalf("incremental update lost its owed seq: %+v %v", owed, err)
				}
			} else if app.LastRedeploy == nil || app.LastRedeploy.Seq != patch.RedeploySeq || app.LastRedeploy.Outcome != outcome {
				t.Fatalf("live outcome not recorded: %+v", app.LastRedeploy)
			}
		})
	}
}

func TestLiveSettingsRedeployPreservesOlderReplacementAndFencesSupersededSeq(t *testing.T) {
	s := dbtest.New(t)
	newRedeployApp(t, s, "owed")
	if _, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "owed", SetMemoryLimitMB: true, MemoryLimitMB: intp(256), ArmRedeploy: true}); err != nil {
		t.Fatal(err)
	}
	patch, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "owed", SetWorkerMaxWorkers: true, WorkerMaxWorkers: 4, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := s.ResolveLiveSettingsRedeploy(context.Background(), nil, "owed", patch, db.RedeployCompleted, ""); err != nil || resolved {
		t.Fatalf("older replacement was cleared: %v %v", resolved, err)
	}
	full, err := s.RedeployNeedsFullCycle(context.Background(), "owed")
	if err != nil || !full {
		t.Fatalf("older replacement no longer owed: %v %v", full, err)
	}
	newRedeployApp(t, s, "superseded")
	first, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "superseded", SetWorkerMaxWorkers: true, WorkerMaxWorkers: 4, ArmRedeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PatchAppSettings(db.PatchAppSettingsParams{Slug: "superseded", SetMemoryLimitMB: true, MemoryLimitMB: intp(256), ArmRedeploy: true}); err != nil {
		t.Fatal(err)
	}
	if resolved, err := s.ResolveLiveSettingsRedeploy(context.Background(), nil, "superseded", first, db.RedeployCompleted, ""); err != nil || resolved {
		t.Fatalf("superseded update was accepted: %v %v", resolved, err)
	}
}
