package api

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
)

func TestRepairingScheduleActivationExcludesScaleAndWarmMutations(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	srv, app := newScaleTestServer(t, "activation-lifecycle-fence", 2, cfg)
	a := seedClaimedActivation(t, srv.store, app)
	now := time.Now().UTC()
	if err := srv.store.DeferScheduleActivation(a.ID, "repairing", "surge retained", now, now); err != nil {
		t.Fatal(err)
	}

	if changed, err := srv.ScaleDown(app.Slug, time.Millisecond); changed || !errors.Is(err, errScheduleActivationInFlight) {
		t.Fatalf("ScaleDown during repair = %v, %v; want false activation-in-flight", changed, err)
	}
	if changed, err := srv.ScaleUp(app.Slug); changed || !errors.Is(err, errScheduleActivationInFlight) {
		t.Fatalf("ScaleUp during repair = %v, %v; want false activation-in-flight", changed, err)
	}
	if changed, err := srv.WarmShrink(app.Slug, 1, time.Millisecond); changed || !errors.Is(err, errScheduleActivationInFlight) {
		t.Fatalf("WarmShrink during repair = %v, %v; want false activation-in-flight", changed, err)
	}
	if changed, err := srv.WarmExpand(app.Slug); changed || !errors.Is(err, errScheduleActivationInFlight) {
		t.Fatalf("WarmExpand during repair = %v, %v; want false activation-in-flight", changed, err)
	}
	restartReq := httptest.NewRequest("PATCH", "/api/apps/"+app.Slug+"/env?restart=true", nil)
	if restarted, err := srv.maybeRestartForChange(restartReq, app, app.Slug); restarted || !errors.Is(err, errScheduleActivationInFlight) {
		t.Fatalf("env restart during repair = %v, %v; want false activation-in-flight", restarted, err)
	}
	fresh, err := srv.store.GetAppByID(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Replicas != 2 {
		t.Fatalf("replica layout changed during repair: %d", fresh.Replicas)
	}
}

func TestCompatibilityQuarantineExcludesEveryAuxiliaryConsumerBoot(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	srv, app := newScaleTestServer(t, "compatibility-quarantine", 2, cfg)
	pending, err := srv.store.BeginDeployment(app.ID, "candidate", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.MarkDeploymentProducerBarrierEntered(pending.ID); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.QuarantineAndFailDeployment(pending.ID, "candidate producer published before consumer failure"); err != nil {
		t.Fatal(err)
	}

	if changed, err := srv.ScaleUp(app.Slug); changed || !errors.Is(err, errCompatibilityQuarantined) {
		t.Fatalf("ScaleUp during quarantine = %v, %v", changed, err)
	}
	if changed, err := srv.WarmExpand(app.Slug); changed || !errors.Is(err, errCompatibilityQuarantined) {
		t.Fatalf("WarmExpand during quarantine = %v, %v", changed, err)
	}
	restartReq := httptest.NewRequest("PATCH", "/api/apps/"+app.Slug+"/env?restart=true", nil)
	if restarted, err := srv.maybeRestartForChange(restartReq, app, app.Slug); restarted || !errors.Is(err, errCompatibilityQuarantined) {
		t.Fatalf("env restart during quarantine = %v, %v", restarted, err)
	}
}
