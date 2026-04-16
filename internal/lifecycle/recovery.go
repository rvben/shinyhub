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
	for _, app := range apps {
		if app.CurrentPID == nil || app.CurrentPort == nil {
			markRecoveryStopped(store, app.Slug)
			continue
		}
		pid := *app.CurrentPID
		port := *app.CurrentPort
		if err := syscall.Kill(pid, 0); err != nil {
			markRecoveryStopped(store, app.Slug)
			continue
		}
		mgr.Adopt(app.Slug, process.ProcessInfo{
			Slug:   app.Slug,
			PID:    pid,
			Port:   port,
			Status: process.StatusRunning,
		}, process.RunHandle{PID: pid})
		targetURL := fmt.Sprintf("http://localhost:%d", port)
		if err := prx.Register(app.Slug, targetURL); err != nil {
			slog.Error("process recovery: register proxy", "slug", app.Slug, "err", err)
			markRecoveryStopped(store, app.Slug)
			continue
		}
		slog.Info("process recovery: re-adopted process", "slug", app.Slug, "pid", pid, "port", port)
	}
}

func recoverDockerProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy, lister ContainerLister, apps []*db.App) {
	portBySlug := make(map[string]int)
	for _, app := range apps {
		if app.CurrentPort != nil {
			portBySlug[app.Slug] = *app.CurrentPort
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
