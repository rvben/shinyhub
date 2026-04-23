package lifecycle

import (
	"fmt"
	"log/slog"
	"strconv"
	"syscall"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// ContainerLister is implemented by DockerRuntime to support recovery.
// NativeRuntime does not implement it; pass nil for native mode.
type ContainerLister interface {
	ListByLabel(labelFilter string) ([]process.ContainerInfo, error)
	InspectPID(containerID string) (int, error)
}

// RecoverProcesses re-adopts running app processes after a server restart.
// For native runtime, pass nil for lister (PID-based recovery is used).
// For docker runtime, pass the DockerRuntime as lister. defaultMaxSessions is
// the runtime-wide session-cap fallback applied when an app has
// max_sessions_per_replica == 0.
func RecoverProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, lister ContainerLister, defaultMaxSessions int) {
	apps, err := store.ListRunningApps()
	if err != nil {
		slog.Error("process recovery: list running apps", "err", err)
		return
	}

	if lister != nil {
		recoverDockerProcesses(store, mgr, prx, lister, apps, defaultMaxSessions)
		return
	}
	recoverNativeProcesses(store, mgr, prx, apps, defaultMaxSessions)
}

func recoverNativeProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, apps []*db.App, defaultMaxSessions int) {
	for _, app := range apps {
		reps, err := store.ListReplicas(app.ID)
		if err != nil || len(reps) == 0 {
			markRecoveryStopped(store, app.Slug)
			continue
		}
		prx.SetPoolSize(app.Slug, app.Replicas)
		prx.SetPoolCap(app.Slug, deploy.ResolveMaxSessionsPerReplica(app.MaxSessionsPerReplica, defaultMaxSessions))
		anyAlive := false
		for _, r := range reps {
			if r.PID == nil {
				// No PID recorded → treat as crashed so the watcher can restart it.
				_ = store.UpsertReplica(db.UpsertReplicaParams{
					AppID: app.ID, Index: r.Index, Status: "crashed",
				})
				continue
			}
			if r.Port == nil {
				// PID but no port → corrupted row. Log and skip without status change.
				slog.Warn("recovery: replica has PID but no port", "slug", app.Slug, "idx", r.Index)
				continue
			}
			if err := syscall.Kill(*r.PID, 0); err != nil {
				_ = store.UpsertReplica(db.UpsertReplicaParams{
					AppID: app.ID, Index: r.Index, Status: "crashed",
				})
				continue
			}
			mgr.Adopt(app.Slug, process.ProcessInfo{
				Slug:   app.Slug,
				Index:  r.Index,
				PID:    *r.PID,
				Port:   *r.Port,
				Status: process.StatusRunning,
			}, process.RunHandle{PID: *r.PID})
			targetURL := fmt.Sprintf("http://localhost:%d", *r.Port)
			if err := prx.RegisterReplica(app.Slug, r.Index, targetURL); err != nil {
				slog.Error("process recovery: register proxy", "slug", app.Slug, "idx", r.Index, "err", err)
				continue
			}
			anyAlive = true
			slog.Info("process recovery: re-adopted process", "slug", app.Slug, "idx", r.Index, "pid", *r.PID)
		}
		if !anyAlive {
			markRecoveryStopped(store, app.Slug)
		}
	}
}

func recoverDockerProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, lister ContainerLister, apps []*db.App, defaultMaxSessions int) {
	// Index apps by slug for fast lookup; configure proxy pool sizes up front.
	// Also pre-fetch replicas for each app so the adoption loop avoids N*M DB reads.
	bySlug := make(map[string]*db.App, len(apps))
	replicasByApp := make(map[int64][]*db.Replica, len(apps))
	for _, a := range apps {
		bySlug[a.Slug] = a
		prx.SetPoolSize(a.Slug, a.Replicas)
		prx.SetPoolCap(a.Slug, deploy.ResolveMaxSessionsPerReplica(a.MaxSessionsPerReplica, defaultMaxSessions))
		reps, err := store.ListReplicas(a.ID)
		if err != nil {
			slog.Error("recovery: list replicas", "slug", a.Slug, "err", err)
			continue
		}
		replicasByApp[a.ID] = reps
	}

	containers, err := lister.ListByLabel(`{"label":["shinyhub.slug"]}`)
	if err != nil {
		slog.Error("recovery: list docker containers", "err", err)
		for _, a := range apps {
			markRecoveryStopped(store, a.Slug)
		}
		return
	}

	type candidate struct {
		slug string
		idx  int
		pid  int
		cID  string
	}
	var alive []candidate

	for _, c := range containers {
		slug := c.Labels["shinyhub.slug"]
		idxStr := c.Labels["shinyhub.replica_index"]
		app, ok := bySlug[slug]
		if !ok {
			continue // orphan container; leave alone
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			slog.Warn("recovery: bad replica_index label", "slug", slug, "label", idxStr)
			continue
		}
		if idx >= app.Replicas {
			slog.Warn("recovery: container index beyond current pool; skipping", "slug", slug, "idx", idx, "pool", app.Replicas)
			continue
		}
		pid, err := lister.InspectPID(c.ID)
		if err != nil {
			slog.Error("recovery: inspect docker container", "slug", slug, "idx", idx, "err", err)
			continue
		}
		alive = append(alive, candidate{slug, idx, pid, c.ID})
	}

	touched := make(map[string]bool)
	for _, r := range alive {
		app := bySlug[r.slug]
		var port int
		for _, rep := range replicasByApp[app.ID] {
			if rep.Index == r.idx && rep.Port != nil {
				port = *rep.Port
				break
			}
		}
		if port == 0 {
			slog.Warn("recovery: no port row for adopted container", "slug", r.slug, "idx", r.idx)
			continue
		}
		mgr.Adopt(r.slug, process.ProcessInfo{
			Slug:   r.slug,
			Index:  r.idx,
			PID:    r.pid,
			Port:   port,
			Status: process.StatusRunning,
		}, process.RunHandle{ContainerID: r.cID})
		targetURL := fmt.Sprintf("http://localhost:%d", port)
		if err := prx.RegisterReplica(r.slug, r.idx, targetURL); err != nil {
			slog.Error("recovery: register docker proxy", "slug", r.slug, "idx", r.idx, "err", err)
			continue
		}
		touched[r.slug] = true
		slog.Info("recovery: adopted docker container", "slug", r.slug, "idx", r.idx, "pid", r.pid)
	}

	for _, a := range apps {
		if !touched[a.Slug] {
			markRecoveryStopped(store, a.Slug)
		}
	}
}

func markRecoveryStopped(store *db.Store, slug string) {
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
		slog.Error("process recovery: mark stopped", "slug", slug, "err", err)
	}
}
