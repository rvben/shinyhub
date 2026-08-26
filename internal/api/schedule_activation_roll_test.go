package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/activation"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

type activationTestSampler struct{ stats process.Stats }

func (s activationTestSampler) Sample(process.RunHandle) (process.Stats, error) { return s.stats, nil }

type activationDockerContainer struct {
	info process.ContainerInfo
	pid  int
	done chan struct{}
	once sync.Once
}

type activationDockerRuntime struct {
	mu             sync.Mutex
	nextID         int
	containers     map[string]*activationDockerContainer
	lastListFilter string
	listErr        error
	removeErr      error
}

func newActivationDockerRuntime() *activationDockerRuntime {
	return &activationDockerRuntime{containers: make(map[string]*activationDockerContainer)}
}

func (r *activationDockerRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := fmt.Sprintf("activation-container-%d", r.nextID)
	pid := 80000 + r.nextID
	r.containers[id] = &activationDockerContainer{
		info: process.ContainerInfo{ID: id, Labels: map[string]string{
			process.LabelManaged:      "true",
			process.LabelSlug:         p.Slug,
			process.LabelReplicaIndex: strconv.Itoa(p.Index),
			process.LabelTier:         p.Tier,
			process.LabelProvider:     "docker",
			process.LabelDeploymentID: strconv.FormatInt(p.DeploymentID, 10),
			process.LabelAppVersion:   p.AppVersion,
		}},
		pid: pid, done: make(chan struct{}),
	}
	return process.ReplicaEndpoint{
		URL: fmt.Sprintf("http://127.0.0.1:%d", p.Port), Provider: "docker",
		WorkerID: id,
		Handle:   process.RunHandle{PID: pid, ContainerID: id},
	}, nil
}

func (r *activationDockerRuntime) Signal(h process.RunHandle, _ syscall.Signal) error {
	r.mu.Lock()
	c := r.containers[h.ContainerID]
	r.mu.Unlock()
	if c == nil {
		return nil
	}
	c.once.Do(func() { close(c.done) })
	return nil
}
func (r *activationDockerRuntime) Wait(ctx context.Context, h process.RunHandle) error {
	r.mu.Lock()
	c := r.containers[h.ContainerID]
	r.mu.Unlock()
	if c == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return nil
	}
}
func (*activationDockerRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}
func (*activationDockerRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (*activationDockerRuntime) HostPreparesDeps() bool    { return false }
func (*activationDockerRuntime) AppBindHost() string       { return "0.0.0.0" }
func (*activationDockerRuntime) HostProvidesAppData() bool { return true }
func (r *activationDockerRuntime) ListByLabel(filter string) ([]process.ContainerInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastListFilter = filter
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]process.ContainerInfo, 0, len(r.containers))
	for _, c := range r.containers {
		out = append(out, c.info)
	}
	return out, nil
}
func (r *activationDockerRuntime) InspectPID(id string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.containers[id]; c != nil {
		return c.pid, nil
	}
	return 0, fmt.Errorf("container %s not found", id)
}
func (r *activationDockerRuntime) RemoveContainer(id string) error {
	return r.RemoveHandle(process.RunHandle{ContainerID: id})
}
func (r *activationDockerRuntime) RemoveHandle(h process.RunHandle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.removeErr != nil {
		return r.removeErr
	}
	if c := r.containers[h.ContainerID]; c != nil {
		c.once.Do(func() { close(c.done) })
		delete(r.containers, h.ContainerID)
	}
	return nil
}
func (r *activationDockerRuntime) addOrphan(slug string, index int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	id := fmt.Sprintf("activation-container-%d", r.nextID)
	r.containers[id] = &activationDockerContainer{info: process.ContainerInfo{ID: id, Labels: map[string]string{
		process.LabelManaged: "true", process.LabelSlug: slug, process.LabelReplicaIndex: strconv.Itoa(index),
	}}, pid: 80000 + r.nextID, done: make(chan struct{})}
	return id
}

func TestScheduleActivationRoll_MixedTierRecoveredSurgeFencesLaunchUntilDockerAbsenceProved(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Runtime.Tiers = []config.TierConfig{
		{Name: "local", Runtime: "native"},
		{Name: "burst", Runtime: "docker"},
	}
	srv, app := newScaleTestServer(t, "mixed-tier-recovered-surge", 2, cfg)
	if err := srv.store.SetAppPlacement(app.ID, `{"local":1,"burst":1}`, 2); err != nil {
		t.Fatal(err)
	}
	runtime := newActivationDockerRuntime()
	containerID := runtime.addOrphan(app.Slug, 2)
	srv.manager.RegisterRuntime("burst", runtime)
	a := seedClaimedActivation(t, srv.store, app)
	deps, err := srv.store.ListDeployments(app.ID)
	if err != nil || len(deps) == 0 {
		t.Fatalf("deployments=%v err=%v", deps, err)
	}
	runtime.mu.Lock()
	pid := runtime.containers[containerID].pid
	runtime.mu.Unlock()
	port := 21920
	if err := srv.store.UpsertActivationReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 2, PID: &pid, Port: &port, Status: "crashed",
		Provider: "docker", Tier: "burst", EndpointURL: "http://127.0.0.1:21920", WorkerID: containerID,
		AppVersion: deps[0].Version, DesiredState: "running", DeploymentID: &deps[0].ID,
	}, a.TargetGeneration, a.ID); err != nil {
		t.Fatal(err)
	}
	starts := 0
	srv.deployReplica = func(deploy.Params, int) (*deploy.Result, error) {
		starts++
		return nil, errors.New("must not launch before old container absence is proved")
	}

	runtime.listErr = errors.New("daemon inventory unavailable")
	err = srv.Roll(context.Background(), a)
	var repair *activation.RepairRequiredError
	if !errors.As(err, &repair) {
		t.Fatalf("inventory-failure Roll error=%v, want repair required", err)
	}
	if starts != 0 {
		t.Fatalf("inventory failure allowed %d replacement starts", starts)
	}

	runtime.listErr = nil
	runtime.removeErr = errors.New("daemon refused removal")
	err = srv.Roll(context.Background(), a)
	if !errors.As(err, &repair) {
		t.Fatalf("removal-failure Roll error=%v, want repair required", err)
	}
	if starts != 0 {
		t.Fatalf("removal failure allowed %d replacement starts", starts)
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == 2 {
			if row.Tier != "burst" || row.WorkerID != containerID || row.PID == nil || *row.PID != pid {
				t.Fatalf("recovered surge identity changed after failed proof: %+v", row)
			}
			return
		}
	}
	t.Fatal("recovered surge row disappeared after failed proof")
}

func TestScheduleActivationRoll_SurgesThenReplacesEveryCanonicalSlot(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 100 * time.Millisecond
	srv, app := newScaleTestServer(t, "fresh", 2, cfg)
	srv.proxy.SetPoolSize(app.Slug, app.Replicas)

	for index := 0; index < app.Replicas; index++ {
		if _, err := srv.manager.Start(process.StartParams{
			Slug: app.Slug, Index: index, Dir: t.TempDir(),
			Command: []string{"sleep", "30"}, Port: 20100 + index,
		}); err != nil {
			t.Fatalf("seed replica %d: %v", index, err)
		}
		if err := srv.proxy.RegisterReplica(app.Slug, index, fmt.Sprintf("http://127.0.0.1:%d", 20100+index), nil, 0); err != nil {
			t.Fatalf("register seed %d: %v", index, err)
		}
	}

	var bootOrder []int
	reservationByIndex := map[int]bool{}
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		port := 20200 + index
		info, err := srv.manager.Start(process.StartParams{
			Slug: app.Slug, Index: index, Dir: t.TempDir(),
			Command: []string{"sleep", "30"}, Port: port,
			LaunchReservationHeld: p.LaunchReservationHeld, GuardUntilAcknowledged: p.GuardUntilAcknowledged,
		})
		if err != nil {
			return nil, err
		}
		bootOrder = append(bootOrder, index)
		reservationByIndex[index] = p.LaunchReservationHeld
		endpoint := info.EndpointURL
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
		if err := srv.proxy.RegisterReplica(app.Slug, index, endpoint, nil, 1); err != nil {
			return nil, err
		}
		return &deploy.Result{Index: index, PID: info.PID, Port: port, Provider: "native", Tier: "default", EndpointURL: endpoint}, nil
	}

	a := seedClaimedActivation(t, srv.store, app)
	if err := srv.Roll(context.Background(), a); err != nil {
		t.Fatalf("Roll: %v", err)
	}
	if fmt.Sprint(bootOrder) != "[2 0 1]" {
		t.Fatalf("boot order = %v, want surge then slots [2 0 1]", bootOrder)
	}
	if !reservationByIndex[2] || reservationByIndex[0] || reservationByIndex[1] {
		t.Fatalf("launch reservation flags=%v, want surge=true and canonical=false", reservationByIndex)
	}
	got, err := srv.store.GetAppBySlug(app.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if got.Replicas != 2 {
		t.Fatalf("configured replicas = %d, want unchanged 2", got.Replicas)
	}
	if _, ok := srv.manager.GetReplica(app.Slug, 2); ok {
		t.Fatal("surge replica remained after successful activation")
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index < 2 && (row.DataGeneration != a.TargetGeneration || row.ActivationID == nil || *row.ActivationID != a.ID) {
			t.Errorf("slot %d attribution = generation %d activation %v", row.Index, row.DataGeneration, row.ActivationID)
		}
	}
}

func TestScheduleActivationRoll_FailedSurgeLeavesServingReplicaUntouched(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	srv, app := newScaleTestServer(t, "fresh", 1, cfg)
	info, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20300,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:20300", nil, 0); err != nil {
		t.Fatal(err)
	}
	srv.deployReplica = func(deploy.Params, int) (*deploy.Result, error) {
		return nil, errors.New("health check failed")
	}

	a := seedClaimedActivation(t, srv.store, app)
	err = srv.Roll(context.Background(), a)
	var retryable *activation.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("Roll error = %v, want retryable surge failure", err)
	}
	after, ok := srv.manager.GetReplica(app.Slug, 0)
	if !ok || after.PID != info.PID || after.Status != process.StatusRunning {
		t.Fatalf("serving replica changed after failed surge: before=%+v after=%+v ok=%v", info, after, ok)
	}
	if srv.proxy.ReplicaTargetURL(app.Slug, 0) == "" {
		t.Fatal("serving replica was deregistered after failed surge")
	}
}

func TestScheduleActivationRoll_DoesNotWakeStoppedApp(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	srv, app := newScaleTestServer(t, "stopped", 1, cfg)
	if err := srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	booted := false
	srv.deployReplica = func(deploy.Params, int) (*deploy.Result, error) {
		booted = true
		return nil, errors.New("must not boot")
	}
	a := seedClaimedActivation(t, srv.store, app)
	err := srv.Roll(context.Background(), a)
	if !errors.Is(err, activation.ErrNotNeeded) {
		t.Fatalf("Roll error = %v, want ErrNotNeeded", err)
	}
	if booted {
		t.Fatal("activation booted an intentionally stopped app")
	}
}

func TestScheduleActivationRoll_InvalidatesHibernatedSnapshotWithoutChangingIntent(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	srv, app := newScaleTestServer(t, "sleeping", 1, cfg)
	info, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20350,
	})
	if err != nil {
		t.Fatal(err)
	}
	pid, port := info.PID, info.Port
	if err := srv.store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: db.ReplicaStatusSuspended,
		DesiredState: "stopped", EndpointURL: info.EndpointURL,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	a := seedClaimedActivation(t, srv.store, app)
	if err := srv.Roll(context.Background(), a); !errors.Is(err, activation.ErrNotNeeded) {
		t.Fatalf("Roll error = %v, want ErrNotNeeded", err)
	}
	if _, ok := srv.manager.GetReplica(app.Slug, 0); ok {
		t.Fatal("hibernated process image remained resumable")
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("replicas=%v err=%v", rows, err)
	}
	if rows[0].Status != "stopped" || rows[0].DesiredState != "stopped" || rows[0].PID != nil || rows[0].DataGeneration != a.TargetGeneration {
		t.Fatalf("invalidated snapshot = %+v, want stopped intent with no PID at generation %d", rows[0], a.TargetGeneration)
	}
}

func TestScheduleActivationRoll_PostDetachFailureKeepsFreshSurgeForRepair(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 10 * time.Millisecond
	srv, app := newScaleTestServer(t, "repair", 1, cfg)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20400,
	}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:20400", nil, 0); err != nil {
		t.Fatal(err)
	}
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		if index == 0 {
			return nil, errors.New("replacement health check failed")
		}
		port := 20400 + index
		info, err := srv.manager.Start(process.StartParams{
			Slug: app.Slug, AppID: app.ID, Index: index, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: port,
			Tier: p.DefaultTier, AppVersion: p.AppVersion, DeploymentID: p.DeploymentID, ContentDigest: p.ContentDigest,
			LaunchReservationHeld: p.LaunchReservationHeld, GuardUntilAcknowledged: p.GuardUntilAcknowledged,
		})
		if err != nil {
			return nil, err
		}
		endpoint := info.EndpointURL
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
		if err := srv.proxy.RegisterReplica(app.Slug, index, endpoint, nil, 1); err != nil {
			return nil, err
		}
		return &deploy.Result{Index: index, PID: info.PID, Port: port, Provider: "native", Tier: "default", EndpointURL: endpoint}, nil
	}

	a := seedClaimedActivation(t, srv.store, app)
	a.Attempts = 3 // prove the ordinary retry budget cannot retire the safety route
	err := srv.Roll(context.Background(), a)
	var repair *activation.RepairRequiredError
	if !errors.As(err, &repair) {
		t.Fatalf("Roll error = %v, want RepairRequiredError", err)
	}
	if info, ok := srv.manager.GetReplica(app.Slug, 1); !ok || info.Status != process.StatusRunning {
		t.Fatalf("fresh surge was removed during repair: %+v ok=%v", info, ok)
	}
	if srv.proxy.ReplicaTargetURL(app.Slug, 1) == "" {
		t.Fatal("fresh surge route was removed during repair")
	}
}

func TestScheduleActivationRoll_GenericUnconfirmedReplacementCleanupPreservesDurableRuntimeIdentity(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 10 * time.Millisecond
	srv, app := newScaleTestServer(t, "unconfirmed-replacement", 1, cfg)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20500,
	}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:20500", nil, 0); err != nil {
		t.Fatal(err)
	}

	guardedPID := os.Getpid()
	canonicalStarts := 0
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		if index == 0 {
			canonicalStarts++
			started := deploy.Result{Index: index, PID: guardedPID, Port: 20510, Provider: "native", Tier: "default", EndpointURL: "http://127.0.0.1:20510"}
			if err := p.ReplicaStarted(started); err != nil {
				return nil, err
			}
			return nil, &deploy.ReplicaStartError{
				Cause:            errors.New("health: not ready"),
				CleanupError:     errors.New("sigterm: injected signal failure"),
				CleanupConfirmed: false,
			}
		}
		port := 20500 + index
		info, err := srv.manager.Start(process.StartParams{
			Slug: app.Slug, Index: index, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: port,
			LaunchReservationHeld: p.LaunchReservationHeld, GuardUntilAcknowledged: p.GuardUntilAcknowledged,
		})
		if err != nil {
			return nil, err
		}
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		if err := srv.proxy.RegisterReplica(app.Slug, index, endpoint, nil, 1); err != nil {
			return nil, err
		}
		result := deploy.Result{Index: index, PID: info.PID, Port: port, Provider: info.Provider, Tier: info.Tier, EndpointURL: endpoint, WorkerID: info.WorkerID}
		if err := p.ReplicaStarted(result); err != nil {
			return nil, err
		}
		return &result, nil
	}

	a := seedClaimedActivation(t, srv.store, app)
	err := srv.Roll(context.Background(), a)
	var repair *activation.RepairRequiredError
	if !errors.As(err, &repair) {
		t.Fatalf("Roll error=%v, want repair required", err)
	}
	if errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("Roll error=%v unexpectedly uses ErrStopUnconfirmed; test requires generic cleanup failure", err)
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundCanonical := false
	for _, row := range rows {
		if row.Index == 0 {
			foundCanonical = true
			if row.Status != "starting" || row.PID == nil || *row.PID != guardedPID || row.Provider != "native" || row.EndpointURL == "" {
				t.Fatalf("guarded replacement identity was erased: %+v", row)
			}
			break
		}
	}
	if !foundCanonical {
		t.Fatal("canonical replacement row missing")
	}

	// Simulate a coordinator retry after losing its in-memory runtime entry.
	// The durable live PID must fence a second replacement start.
	err = srv.Roll(context.Background(), a)
	if !errors.As(err, &repair) {
		t.Fatalf("retry Roll error=%v, want repair required", err)
	}
	if canonicalStarts != 1 {
		t.Fatalf("canonical replacement starts=%d, want 1; retry launched over quarantined PID", canonicalStarts)
	}
}

func TestScheduleActivationRoll_ConfirmedFailedStartCanRetryImmediately(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 5 * time.Millisecond
	srv, app := newScaleTestServer(t, "confirmed-start-retry", 1, cfg)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, AppID: app.ID, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20520,
	}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:20520", nil, 0); err != nil {
		t.Fatal(err)
	}

	const stoppedPID = 2147483647
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		started := deploy.Result{Index: index, PID: stoppedPID, Port: 20521, Provider: "native", Tier: "default", EndpointURL: "http://127.0.0.1:20521"}
		if err := p.ReplicaStarted(started); err != nil {
			return nil, err
		}
		return nil, &deploy.ReplicaStartError{Cause: errors.New("health: not ready"), CleanupConfirmed: true}
	}
	a := seedClaimedActivation(t, srv.store, app)
	var retry *activation.RetryableError
	if err := srv.Roll(context.Background(), a); !errors.As(err, &retry) {
		t.Fatalf("first Roll error=%v, want retryable after confirmed cleanup", err)
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == 1 && (row.PID != nil || row.WorkerID != "" || row.Status != "stopped") {
			t.Fatalf("confirmed cleanup identity not tombstoned: %+v", row)
		}
	}

	srv.deployReplica = successfulActivationTestDeployer(t, srv, app, 20530)
	if err := srv.Roll(context.Background(), a); err != nil {
		t.Fatalf("immediate retry after confirmed cleanup: %v", err)
	}
}

func TestScheduleActivationRoll_RecoversAfterConfirmedStopCheckpointFailure(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 5 * time.Millisecond
	srv, app := newScaleTestServer(t, "cleanup-checkpoint-retry", 1, cfg)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, AppID: app.ID, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20540,
	}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:20540", nil, 0); err != nil {
		t.Fatal(err)
	}
	a := seedClaimedActivation(t, srv.store, app)
	dropFailure := installDBFailureTrigger(t, srv.store, dbFailureTrigger{
		name:      "fail_activation_identity_clear",
		table:     "replicas",
		event:     "UPDATE",
		condition: "OLD.app_id = " + strconv.FormatInt(app.ID, 10) + " AND OLD.idx = 1 AND NEW.status = 'stopped'",
	})
	const stoppedPID = 2147483647
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		started := deploy.Result{Index: index, PID: stoppedPID, Port: 20541, Provider: "native", Tier: "default", EndpointURL: "http://127.0.0.1:20541"}
		if err := p.ReplicaStarted(started); err != nil {
			return nil, err
		}
		return nil, &deploy.ReplicaStartError{Cause: errors.New("register: injected failure"), CleanupConfirmed: true}
	}
	var repair *activation.RepairRequiredError
	if err := srv.Roll(context.Background(), a); !errors.As(err, &repair) {
		t.Fatalf("Roll error=%v, want repair when stop checkpoint fails", err)
	}
	dropFailure()
	srv.deployReplica = successfulActivationTestDeployer(t, srv, app, 20550)
	if err := srv.Roll(context.Background(), a); err != nil {
		t.Fatalf("repair could not prove stopped PID absent and retry: %v", err)
	}
}

func TestDiscardInvalidActivationSurge_QuarantinesDurableIdentityMissingFromManager(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	srv, app := newScaleTestServer(t, "quarantined-surge", 1, cfg)
	activationID := int64(99)
	pid, port := os.Getpid(), 20601
	if err := srv.store.UpsertActivationReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: app.Replicas, PID: &pid, Port: &port, Status: "starting",
		Provider: "native", Tier: "default", EndpointURL: "http://127.0.0.1:20601",
		AppVersion: "v1", DesiredState: "running",
	}, 1, activationID); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, app.Replicas+1)
	if err := srv.proxy.RegisterReplica(app.Slug, app.Replicas, "http://127.0.0.1:20601", nil, 0); err != nil {
		t.Fatal(err)
	}

	err := srv.discardInvalidActivationSurge(app, app.Replicas)
	if !errors.Is(err, process.ErrStopUnconfirmed) {
		t.Fatalf("discard error=%v, want ErrStopUnconfirmed quarantine", err)
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == app.Replicas {
			if row.PID == nil || *row.PID != pid || row.Status != "starting" {
				t.Fatalf("quarantined identity changed: %+v", row)
			}
			if srv.proxy.ReplicaTargetURL(app.Slug, app.Replicas) == "" {
				t.Fatal("quarantined route was removed before runtime absence was proven")
			}
			return
		}
	}
	t.Fatal("quarantined surge row was deleted")
}

func TestScheduleActivationRoll_QuarantinesCanonicalIdentityMissingFromManager(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 5 * time.Millisecond
	srv, app := newScaleTestServer(t, "canonical-quarantine", 1, cfg)
	a := seedClaimedActivation(t, srv.store, app)
	if err := srv.store.UpdateScheduleActivationProgress(a.ID, "surge_ready", 1, 0); err != nil {
		t.Fatal(err)
	}
	a.SurgeIndex = 1
	deps, err := srv.store.ListDeployments(app.ID)
	if err != nil || len(deps) == 0 {
		t.Fatalf("deployments=%v err=%v", deps, err)
	}
	pid, port, depID := os.Getpid(), 21600, deps[0].ID
	if err := srv.store.UpsertActivationReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "starting",
		Provider: "native", Tier: "local", EndpointURL: "http://127.0.0.1:21600",
		AppVersion: deps[0].Version, DesiredState: "running", DeploymentID: &depID,
	}, a.TargetGeneration, a.ID); err != nil {
		t.Fatal(err)
	}
	surge, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, AppID: app.ID, Index: 1, Dir: t.TempDir(), Command: []string{"sleep", "30"},
		Port: 21601, AppVersion: deps[0].Version, DeploymentID: depID,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 2)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:21600", nil, depID); err != nil {
		t.Fatal(err)
	}
	if err := srv.proxy.RegisterReplica(app.Slug, 1, surge.EndpointURL, nil, depID); err != nil {
		t.Fatal(err)
	}
	if err := srv.persistActivationReplica(app, deps[0], &deploy.Result{
		Index: 1, PID: surge.PID, Port: surge.Port, Provider: surge.Provider, Tier: surge.Tier,
		EndpointURL: surge.EndpointURL, WorkerID: surge.WorkerID,
	}, a); err != nil {
		t.Fatal(err)
	}
	deployCalled := false
	srv.deployReplica = func(deploy.Params, int) (*deploy.Result, error) {
		deployCalled = true
		return nil, errors.New("must not deploy over quarantined identity")
	}

	err = srv.Roll(context.Background(), a)
	var repair *activation.RepairRequiredError
	if !errors.As(err, &repair) {
		t.Fatalf("Roll error=%v, want repair for unconfirmed canonical stop", err)
	}
	if deployCalled {
		t.Fatal("replacement started over an unconfirmed canonical PID")
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == 0 && row.PID != nil && *row.PID == pid && row.Status == "starting" {
			return
		}
	}
	t.Fatalf("canonical quarantine identity was erased: %+v", rows)
}

func TestConfirmActivationReplicaStopped_DockerAbsenceIsProvenWithoutSyntheticPIDQuarantine(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "docker"
	srv, app := newScaleTestServer(t, "docker-absence", 1, cfg)
	runtime := newActivationDockerRuntime()
	srv.manager = process.NewManager(t.TempDir(), runtime)
	pid, port := 0, 21650
	if err := srv.store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, PID: &pid, Port: &port, Status: "running",
		Provider: "docker", Tier: "local", WorkerID: "already-gone-container",
		EndpointURL: "http://127.0.0.1:21650", DesiredState: "running",
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.confirmActivationReplicaStopped(app, 1); err != nil {
		t.Fatalf("confirmed Docker absence: %v", err)
	}
	runtime.mu.Lock()
	gotFilter := runtime.lastListFilter
	runtime.mu.Unlock()
	if gotFilter != process.ManagedContainerFilterJSON {
		t.Fatalf("Docker absence filter = %q, want %q", gotFilter, process.ManagedContainerFilterJSON)
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == 1 && (row.PID != nil || row.WorkerID != "" || row.Status != "stopped") {
			t.Fatalf("Docker absence did not leave a stop tombstone: %+v", row)
		}
	}
}

func TestScheduleActivationRoll_PostSurgeCheckpointFailuresStayInRepair(t *testing.T) {
	cases := []struct {
		name      string
		table     string
		event     string
		condition string
	}{
		{
			name:      "persist surge identity",
			table:     "replicas",
			event:     "UPDATE",
			condition: "NEW.idx = 1 AND NEW.status = 'running'",
		},
		{
			name:      "record surge ready",
			table:     "schedule_activations",
			event:     "UPDATE OF phase",
			condition: "NEW.phase = 'surge_ready'",
		},
		{
			name:      "record draining slot",
			table:     "schedule_activations",
			event:     "UPDATE OF phase",
			condition: "NEW.phase = 'draining_slot'",
		},
		{
			name:      "record starting slot",
			table:     "schedule_activations",
			event:     "UPDATE OF phase",
			condition: "NEW.phase = 'starting_slot'",
		},
		{
			name:      "persist canonical replacement",
			table:     "replicas",
			event:     "UPDATE",
			condition: "NEW.idx = 0 AND NEW.status = 'running' AND NEW.activation_id IS NOT NULL",
		},
		{
			name:      "record retiring surge",
			table:     "schedule_activations",
			event:     "UPDATE OF phase",
			condition: "NEW.phase = 'retiring_surge'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Runtime.Mode = "native"
			cfg.Runtime.Docker.DefaultMemoryMB = 64
			cfg.Server.DrainTimeout = 5 * time.Millisecond
			srv, app := newScaleTestServer(t, "checkpoint-repair", 1, cfg)
			if _, err := srv.manager.Start(process.StartParams{
				Slug: app.Slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20700,
			}); err != nil {
				t.Fatal(err)
			}
			srv.proxy.SetPoolSize(app.Slug, 1)
			if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:20700", nil, 0); err != nil {
				t.Fatal(err)
			}
			srv.deployReplica = successfulActivationTestDeployer(t, srv, app, 20800)
			a := seedClaimedActivation(t, srv.store, app)
			a.Attempts = 99
			installDBFailureTrigger(t, srv.store, dbFailureTrigger{
				name: "fail_activation_checkpoint", table: tc.table,
				event: tc.event, condition: tc.condition,
			})

			err := srv.Roll(context.Background(), a)
			var repair *activation.RepairRequiredError
			if !errors.As(err, &repair) {
				t.Fatalf("Roll error=%v, want RepairRequiredError after durable surge start", err)
			}
			if info, ok := srv.manager.GetReplica(app.Slug, app.Replicas); !ok || info.Status != process.StatusRunning {
				t.Fatalf("surge not retained for repair: %+v ok=%v", info, ok)
			}
			if srv.proxy.ReplicaTargetURL(app.Slug, app.Replicas) == "" {
				t.Fatal("surge route not retained for repair")
			}
		})
	}
}

func TestScheduleActivationRoll_FailedFinalDeleteLeavesRetryableStopTombstone(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 5 * time.Millisecond
	srv, app := newScaleTestServer(t, "retire-tombstone", 1, cfg)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 21400,
	}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:21400", nil, 0); err != nil {
		t.Fatal(err)
	}
	srv.deployReplica = successfulActivationTestDeployer(t, srv, app, 21500)
	a := seedClaimedActivation(t, srv.store, app)
	a.Attempts = 99
	dropFailure := installDBFailureTrigger(t, srv.store, dbFailureTrigger{
		name: "fail_surge_delete", table: "replicas", event: "DELETE", condition: "OLD.idx = 1",
	})
	var repair *activation.RepairRequiredError
	if err := srv.Roll(context.Background(), a); !errors.As(err, &repair) {
		t.Fatalf("Roll error=%v, want repair after final delete failure", err)
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == 1 && (row.Status != "stopped" || row.PID != nil || row.EndpointURL != "") {
			t.Fatalf("failed final delete did not leave a safe stop tombstone: %+v", row)
		}
	}
	dropFailure()
	if err := srv.Roll(context.Background(), a); err != nil {
		t.Fatalf("repair retry could not finish stop tombstone cleanup: %v", err)
	}
	rows, err = srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == 1 {
			t.Fatalf("surge tombstone remained after repair retry: %+v", row)
		}
	}
}

func successfulActivationTestDeployer(t *testing.T, srv *Server, app *db.App, portBase int) func(deploy.Params, int) (*deploy.Result, error) {
	t.Helper()
	return func(p deploy.Params, index int) (*deploy.Result, error) {
		port := portBase + index
		info, err := srv.manager.Start(process.StartParams{
			Slug: app.Slug, AppID: app.ID, Index: index, Tier: p.DefaultTier,
			Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: port,
			AppVersion: p.AppVersion, DeploymentID: p.DeploymentID, ContentDigest: p.ContentDigest,
			LaunchReservationHeld: p.LaunchReservationHeld, GuardUntilAcknowledged: p.GuardUntilAcknowledged,
		})
		if err != nil {
			return nil, err
		}
		endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
		if err := srv.proxy.RegisterReplica(app.Slug, index, endpoint, nil, 1); err != nil {
			return nil, err
		}
		result := deploy.Result{Index: index, PID: info.PID, Port: port, Provider: info.Provider, Tier: info.Tier, EndpointURL: endpoint, WorkerID: info.WorkerID}
		if p.ReplicaStarted != nil {
			if err := p.ReplicaStarted(result); err != nil {
				return nil, err
			}
		}
		return &result, nil
	}
}

func TestActivationSurgeMemoryFallsBackToAnotherLiveCanonicalReplica(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	srv, app := newScaleTestServer(t, "capacity-fallback", 2, cfg)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, Index: 1, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 20901,
	}); err != nil {
		t.Fatal(err)
	}
	srv.SetSampler(activationTestSampler{stats: process.Stats{RSSBytes: 100 * 1024 * 1024}})

	if got, want := srv.activationSurgeMemoryMB(app, 0), 189; got != want {
		t.Fatalf("surge memory estimate=%d MiB, want %d MiB from live replica 1", got, want)
	}
}

func TestScheduleActivationRoll_ReleasesHostLaunchReservationBeforeHealthCompletes(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "native"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 5 * time.Millisecond
	srv, app := newScaleTestServer(t, "reservation-release", 1, cfg)
	if _, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, AppID: app.ID, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 21700,
	}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, "http://127.0.0.1:21700", nil, 0); err != nil {
		t.Fatal(err)
	}
	healthBlocked := make(chan struct{})
	allowHealth := make(chan struct{})
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		port := 21710 + index
		info, err := srv.manager.Start(process.StartParams{
			Slug: app.Slug, AppID: app.ID, Index: index, Tier: p.DefaultTier, Dir: t.TempDir(),
			Command: []string{"sleep", "30"}, Port: port, AppVersion: p.AppVersion,
			DeploymentID: p.DeploymentID, LaunchReservationHeld: p.LaunchReservationHeld,
			GuardUntilAcknowledged: p.GuardUntilAcknowledged,
		})
		if err != nil {
			return nil, err
		}
		endpoint := info.EndpointURL
		if endpoint == "" {
			endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		}
		result := deploy.Result{Index: index, PID: info.PID, Port: port, Provider: info.Provider,
			Tier: info.Tier, EndpointURL: endpoint, WorkerID: info.WorkerID}
		if p.ReplicaStarted != nil {
			if err := p.ReplicaStarted(result); err != nil {
				return nil, err
			}
		}
		if err := srv.manager.AcknowledgeReplicaStart(app.Slug, index); err != nil {
			return nil, err
		}
		if index == app.Replicas {
			close(healthBlocked)
			<-allowHealth
		}
		if err := srv.proxy.RegisterReplica(app.Slug, index, endpoint, nil, p.DeploymentID); err != nil {
			return nil, err
		}
		return &result, nil
	}
	a := seedClaimedActivation(t, srv.store, app)
	rollDone := make(chan error, 1)
	go func() { rollDone <- srv.Roll(context.Background(), a) }()
	select {
	case <-healthBlocked:
	case <-time.After(2 * time.Second):
		t.Fatal("surge runtime was not created")
	}
	otherStarted := make(chan error, 1)
	go func() {
		_, err := srv.manager.Start(process.StartParams{
			Slug: "unrelated-app", Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 21799,
		})
		otherStarted <- err
	}()
	select {
	case err := <-otherStarted:
		if err != nil {
			t.Fatalf("unrelated launch after runtime creation: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("host launch reservation remained held during health checking")
	}
	close(allowHealth)
	if err := <-rollDone; err != nil {
		t.Fatalf("roll after health release: %v", err)
	}
}

func TestScheduleActivationRoll_DockerContractRecoversSurgeSweepsOrphansAndCompletes(t *testing.T) {
	cfg := &config.Config{}
	cfg.Runtime.Mode = "docker"
	cfg.Runtime.Docker.DefaultMemoryMB = 64
	cfg.Server.DrainTimeout = 5 * time.Millisecond
	srv, app := newScaleTestServer(t, "docker-activation", 1, cfg)
	runtime := newActivationDockerRuntime()
	srv.manager = process.NewManager(t.TempDir(), runtime)
	dep, err := srv.store.ListDeployments(app.ID)
	if err != nil || len(dep) == 0 {
		t.Fatalf("deployment=%v err=%v", dep, err)
	}
	seed, err := srv.manager.Start(process.StartParams{
		Slug: app.Slug, AppID: app.ID, Index: 0, Tier: "local", Command: []string{"app"}, Port: 21000,
		DeploymentID: dep[0].ID, AppVersion: dep[0].Version, ContentDigest: dep[0].ContentDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	pid, port, depID := seed.PID, seed.Port, dep[0].ID
	if err := srv.store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 0, PID: &pid, Port: &port, Status: "running", Provider: "docker", Tier: "local",
		EndpointURL: seed.EndpointURL, WorkerID: seed.WorkerID, AppVersion: dep[0].Version, DesiredState: "running", DeploymentID: &depID,
	}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.SetPoolSize(app.Slug, 1)
	if err := srv.proxy.RegisterReplica(app.Slug, 0, seed.EndpointURL, nil, depID); err != nil {
		t.Fatal(err)
	}
	srv.deployReplica = successfulActivationTestDeployer(t, srv, app, 21100)
	a := seedClaimedActivation(t, srv.store, app)
	dropFailure := installDBFailureTrigger(t, srv.store, dbFailureTrigger{
		name: "interrupt_after_surge", table: "schedule_activations", event: "UPDATE OF phase",
		condition: "NEW.phase = 'surge_ready'",
	})
	var repair *activation.RepairRequiredError
	if err := srv.Roll(context.Background(), a); !errors.As(err, &repair) {
		t.Fatalf("first Docker roll error=%v, want repairable post-surge interruption", err)
	}
	dropFailure()
	if _, err := srv.store.RequeueRunningScheduleActivations(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	orphanID := runtime.addOrphan(app.Slug, 9)
	sameSlotOrphanID := runtime.addOrphan(app.Slug, app.Replicas)

	recoveredManager := process.NewManager(t.TempDir(), runtime)
	recoveredProxy := proxy.New()
	lifecycle.RecoverProcesses(srv.store, recoveredManager, recoveredProxy, 0, false, "")
	if _, ok := recoveredManager.GetReplica(app.Slug, 0); !ok {
		t.Fatal("Docker canonical replica was not adopted during recovery")
	}
	if _, ok := recoveredManager.GetReplica(app.Slug, app.Replicas); !ok {
		rows, _ := srv.store.ListReplicas(app.ID)
		containers, _ := runtime.ListByLabel("")
		t.Fatalf("activation-owned Docker surge was not adopted during recovery; rows=%+v containers=%+v", rows, containers)
	}
	lifecycle.SweepOrphanContainers(recoveredManager, runtime)
	containers, _ := runtime.ListByLabel("")
	for _, c := range containers {
		if c.ID == orphanID || c.ID == sameSlotOrphanID {
			t.Fatalf("unowned Docker container %s survived exact-identity recovery and orphan sweep", c.ID)
		}
	}

	recoveredServer := New(cfg, srv.store, recoveredManager, recoveredProxy)
	recoveredServer.deployReplica = successfulActivationTestDeployer(t, recoveredServer, app, 21200)
	retry, err := srv.store.ClaimNextScheduleActivation(time.Now().UTC().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if retry.ID != a.ID || retry.Phase != "recovering" {
		t.Fatalf("recovered activation=%+v, want original action in recovery", retry)
	}
	if err := recoveredServer.Roll(context.Background(), retry); err != nil {
		t.Fatalf("recovered Docker roll: %v", err)
	}
	if err := srv.store.FinishScheduleActivation(a.ID, "succeeded", "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, ok := recoveredManager.GetReplica(app.Slug, app.Replicas); ok {
		t.Fatal("Docker surge remained in manager after final cleanup")
	}
	if recoveredProxy.ReplicaTargetURL(app.Slug, app.Replicas) != "" {
		t.Fatal("Docker surge route remained after final cleanup")
	}
	rows, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Index != 0 || rows[0].Provider != "docker" || rows[0].DataGeneration != a.TargetGeneration {
		t.Fatalf("final Docker replica rows=%+v", rows)
	}
	containers, _ = runtime.ListByLabel("")
	if len(containers) != 1 || containers[0].Labels[process.LabelReplicaIndex] != "0" {
		t.Fatalf("final Docker containers=%+v, want only canonical slot 0", containers)
	}
}

func seedClaimedActivation(t *testing.T, store *db.Store, app *db.App) *db.ScheduleActivation {
	t.Helper()
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "refresh", CronExpr: "*/15 * * * *", CommandJSON: `["true"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Second)
	runID, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleID, Status: "running", Trigger: "schedule", StartedAt: now, OnSuccess: "roll",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: runID, Status: "succeeded", FinishedAt: now, ExitCode: activationExitCode(0),
	}); err != nil {
		t.Fatal(err)
	}
	a, err := store.ClaimNextScheduleActivation(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func activationExitCode(v int) *int { return &v }
