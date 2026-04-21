package lifecycle

import (
	"fmt"
	"log/slog"
	"syscall"

	"github.com/rvben/shinyhub/internal/db"
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
// For docker runtime, pass the DockerRuntime as lister.
func RecoverProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, lister ContainerLister) {
	apps, err := store.ListRunningApps()
	if err != nil {
		slog.Error("process recovery: list running apps", "err", err)
		return
	}

	if lister != nil {
		recoverDockerProcesses(store, mgr, prx, lister, apps)
		return
	}
	recoverNativeProcesses(store, mgr, prx, apps)
}

func recoverNativeProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, apps []*db.App) {
	// TODO(Task 12): rewrite to iterate store.ListReplicas per app using the
	// replica table instead of the deprecated per-app PID/port fields.
	for _, app := range apps {
		reps, err := store.ListReplicas(app.ID)
		if err != nil || len(reps) == 0 {
			markRecoveryStopped(store, app.Slug)
			continue
		}
		prx.SetPoolSize(app.Slug, app.Replicas)
		anyAlive := false
		for _, r := range reps {
			if r.PID == nil || r.Port == nil {
				continue
			}
			if err := syscall.Kill(*r.PID, 0); err != nil {
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

func recoverDockerProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, lister ContainerLister, apps []*db.App) {
	// TODO(Task 13): rewrite to use replica table for port lookup.
	portBySlug := make(map[string]int)
	for _, app := range apps {
		reps, err := store.ListReplicas(app.ID)
		if err != nil {
			continue
		}
		for _, r := range reps {
			if r.Port != nil {
				portBySlug[app.Slug] = *r.Port
				break
			}
		}
	}

	containers, err := lister.ListByLabel(`{"label":["shinyhub.slug"]}`)
	if err != nil {
		slog.Error("process recovery: list docker containers", "err", err)
		for _, app := range apps {
			markRecoveryStopped(store, app.Slug)
		}
		return
	}

	recovered := make(map[string]bool)
	for _, c := range containers {
		slug := c.Labels["shinyhub.slug"]
		port, ok := portBySlug[slug]
		if !ok {
			continue
		}
		pid, err := lister.InspectPID(c.ID)
		if err != nil {
			slog.Error("process recovery: inspect docker container", "slug", slug, "err", err)
			markRecoveryStopped(store, slug)
			continue
		}
		mgr.Adopt(slug, process.ProcessInfo{
			Slug:   slug,
			PID:    pid,
			Port:   port,
			Status: process.StatusRunning,
		}, process.RunHandle{ContainerID: c.ID})
		targetURL := fmt.Sprintf("http://localhost:%d", port)
		if err := prx.Register(slug, targetURL); err != nil {
			slog.Error("process recovery: register docker proxy", "slug", slug, "err", err)
			markRecoveryStopped(store, slug)
			continue
		}
		recovered[slug] = true
		slog.Info("process recovery: re-adopted docker container", "slug", slug, "container", c.ID, "port", port)
	}

	for _, app := range apps {
		if !recovered[app.Slug] {
			markRecoveryStopped(store, app.Slug)
		}
	}
}

func markRecoveryStopped(store *db.Store, slug string) {
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
		slog.Error("process recovery: mark stopped", "slug", slug, "err", err)
	}
}
