package lifecycle

import (
	"fmt"
	"log"
	"syscall"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// RecoverProcesses is called once on startup. It queries the DB for apps that
// were running before the server stopped and checks whether their OS processes
// are still alive. Survivors are re-registered in the manager and proxy;
// orphaned entries are marked stopped in the DB.
func RecoverProcesses(store *db.Store, mgr *process.Manager, prx *proxy.Proxy) {
	apps, err := store.ListRunningApps()
	if err != nil {
		log.Printf("process recovery: list running apps: %v", err)
		return
	}
	for _, app := range apps {
		if app.CurrentPID == nil || app.CurrentPort == nil {
			markRecoveryStopped(store, app.Slug)
			continue
		}
		pid := *app.CurrentPID
		port := *app.CurrentPort
		// POSIX-only: Windows is not a supported target.
		if err := syscall.Kill(pid, 0); err != nil {
			// Process is gone.
			markRecoveryStopped(store, app.Slug)
			continue
		}
		// Process is still alive — re-register it.
		mgr.ForceEntry(app.Slug, process.ProcessInfo{
			Slug:   app.Slug,
			PID:    pid,
			Port:   port,
			Status: process.StatusRunning,
		})
		targetURL := fmt.Sprintf("http://localhost:%d", port)
		if err := prx.Register(app.Slug, targetURL); err != nil {
			log.Printf("process recovery: register proxy for %s: %v — marking stopped", app.Slug, err)
			markRecoveryStopped(store, app.Slug)
			continue
		}
		log.Printf("process recovery: re-adopted %s (pid=%d, port=%d)", app.Slug, pid, port)
	}
}

func markRecoveryStopped(store *db.Store, slug string) {
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: "stopped"}); err != nil {
		log.Printf("process recovery: mark stopped %s: %v", slug, err)
	}
}
