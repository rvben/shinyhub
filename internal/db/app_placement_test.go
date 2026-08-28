package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestUpsertReplica_PersistsDeploymentID(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	depID := int64(42)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running", Tier: "burst",
		Provider: "docker", DeploymentID: &depID,
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil || len(reps) != 1 {
		t.Fatalf("ListReplicas: %v len=%d", err, len(reps))
	}
	if reps[0].DeploymentID == nil || *reps[0].DeploymentID != 42 {
		t.Fatalf("DeploymentID = %v, want 42", reps[0].DeploymentID)
	}
}

func TestUpsertReplica_NilDeploymentID(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running",
	}); err != nil {
		t.Fatalf("UpsertReplica: %v", err)
	}
	reps, _ := store.ListReplicas(app.ID)
	if len(reps) != 1 || reps[0].DeploymentID != nil {
		t.Fatalf("expected nil DeploymentID, got %v", reps[0].DeploymentID)
	}
}

func TestUpsertReplica_PrestartProvenanceReplacesActivationAttribution(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	depID := int64(42)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running", DeploymentID: &depID,
		DataGeneration: 7, DataProducerDeploymentID: &depID,
		DataProducerAppVersion: "v2", DataProducerContentDigest: "sha256:v2",
		DataProducerFingerprint: "producer-v2",
	}); err != nil {
		t.Fatal(err)
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil || len(reps) != 1 {
		t.Fatalf("replicas=%+v err=%v", reps, err)
	}
	got := reps[0]
	if got.DataGeneration != 7 || got.ActivationID != nil || got.DataProducerDeploymentID == nil ||
		*got.DataProducerDeploymentID != depID || got.DataProducerAppVersion != "v2" ||
		got.DataProducerContentDigest != "sha256:v2" || got.DataProducerFingerprint != "producer-v2" {
		t.Fatalf("prestart provenance=%+v", got)
	}
}

func TestUpsertReplica_CurrentPublicationIsStampedOnlyAfterFreshConsumerBoot(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "publication-boot", u.ID)
	oldProducer, newProducer := int64(41), int64(42)
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "running", DataGeneration: 6,
		DataProducerDeploymentID: &oldProducer, DataProducerAppVersion: "v1",
		DataProducerContentDigest: "sha256:v1", DataProducerFingerprint: "old",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO app_data_publication
		(app_id, generation, schedule_run_id, producer_deployment_id,
		 producer_app_version, producer_content_digest, producer_fingerprint, published_at)
		VALUES (?, 7, 99, ?, 'v2', 'sha256:v2', 'new', CURRENT_TIMESTAMP)`, app.ID, newProducer); err != nil {
		t.Fatal(err)
	}

	// Process adoption is not a boot: it must retain what that process loaded.
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	adopted, err := store.ListReplicas(app.ID)
	if err != nil || len(adopted) != 1 {
		t.Fatalf("adopted=%+v err=%v", adopted, err)
	}
	if adopted[0].DataGeneration != 6 || adopted[0].DataProducerContentDigest != "sha256:v1" {
		t.Fatalf("adoption falsely advanced provenance: %+v", adopted[0])
	}

	// A fresh boot at the same slot loads the current on-disk publication.
	if err := store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, Status: "starting", ConsumerBooted: true,
	}); err != nil {
		t.Fatal(err)
	}
	booted, err := store.ListReplicas(app.ID)
	if err != nil || len(booted) != 1 {
		t.Fatalf("booted=%+v err=%v", booted, err)
	}
	if booted[0].DataGeneration != 7 || booted[0].DataProducerDeploymentID == nil ||
		*booted[0].DataProducerDeploymentID != newProducer || booted[0].DataProducerAppVersion != "v2" ||
		booted[0].DataProducerContentDigest != "sha256:v2" || booted[0].DataProducerFingerprint != "new" {
		t.Fatalf("fresh boot did not inherit current publication: %+v", booted[0])
	}
	// The crash-safe starting row already carries the data it loaded; the later
	// readiness transition must preserve that attribution without restamping.
	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	ready, err := store.ListReplicas(app.ID)
	if err != nil || len(ready) != 1 || ready[0].DataGeneration != 7 || ready[0].DataProducerContentDigest != "sha256:v2" {
		t.Fatalf("readiness transition lost starting provenance: replicas=%+v err=%v", ready, err)
	}
}

func TestSetAppPlacement_RoundTrip(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if err := store.SetAppPlacement(app.ID, `{"local":1,"burst":2}`, 3); err != nil {
		t.Fatalf("SetAppPlacement: %v", err)
	}
	got, err := store.GetApp("demo")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if got.ReplicaPlacement != `{"local":1,"burst":2}` {
		t.Fatalf("ReplicaPlacement = %q", got.ReplicaPlacement)
	}
	if got.Replicas != 3 {
		t.Fatalf("Replicas total = %d, want 3", got.Replicas)
	}
}

func TestSetAppPlacement_DefaultEmptyOnCreate(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)
	if app.ReplicaPlacement != "" {
		t.Fatalf("new app placement = %q, want empty", app.ReplicaPlacement)
	}
}

func TestReplica_LostStatusRoundTrips(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", u.ID)

	if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: db.ReplicaStatusLost}); err != nil {
		t.Fatalf("upsert lost replica: %v", err)
	}
	reps, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatalf("list replicas: %v", err)
	}
	if len(reps) != 1 || reps[0].Status != db.ReplicaStatusLost {
		t.Fatalf("replica status = %+v, want one row with status %q", reps, db.ReplicaStatusLost)
	}
}

func TestApp_PlacementMap(t *testing.T) {
	cases := []struct {
		name string
		json string
		want map[string]int
	}{
		{"empty is nil", "", nil},
		{"single tier", `{"local":3}`, map[string]int{"local": 3}},
		{"two tiers", `{"local":1,"burst":2}`, map[string]int{"local": 1, "burst": 2}},
		{"malformed is nil", `{not json`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := db.App{ReplicaPlacement: tc.json}
			got := a.PlacementMap()
			if len(got) != len(tc.want) {
				t.Fatalf("PlacementMap() = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("PlacementMap()[%q] = %d, want %d", k, got[k], v)
				}
			}
		})
	}
}
