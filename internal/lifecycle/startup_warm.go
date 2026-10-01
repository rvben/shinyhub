package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// RestoreWarmFloors restores the serving floor of apps that were up before
// recovery but whose processes did not survive. The pre-recovery snapshot is
// deliberate: an operator-slept app must stay asleep across a restart. Apps
// are restored one at a time with at most four concurrent replica boots, using
// the ordinary wake's CAS, compatibility fence, readiness, and crash handling.
func (w *Watcher) RestoreWarmFloors(ctx context.Context, previouslyRunning []*db.App) {
	if w.deploy == nil {
		return
	}
	pending := previouslyRunning
	for len(pending) > 0 {
		var retry []*db.App
		for _, prior := range pending {
			if ctx.Err() != nil || w.isOwner != nil && !w.isOwner() {
				return
			}
			if !isUpStatus(prior.Status) || prior.MinWarmReplicas < 1 || isElasticIsolation(deploy.ResolveWorkerIsolation(prior.WorkerIsolation, w.cfg.DefaultWorkerIsolation)) {
				continue
			}
			release, ok := w.tryAppLease(prior.Slug)
			if !ok {
				retry = append(retry, prior)
				continue
			}
			app, err := w.store.GetAppBySlug(prior.Slug)
			if err != nil {
				if !errors.Is(err, db.ErrNotFound) {
					retry = append(retry, prior)
				}
				release()
				continue
			}
			if app.Status != "hibernated" || app.MinWarmReplicas < 1 || isElasticIsolation(deploy.ResolveWorkerIsolation(app.WorkerIsolation, w.cfg.DefaultWorkerIsolation)) {
				release()
				continue
			}
			won, err := w.store.BeginWake(prior.Slug)
			release()
			if err != nil {
				slog.Warn("startup warm floor: begin wake failed", "slug", prior.Slug, "err", err)
				retry = append(retry, prior)
				continue
			}
			if !won {
				continue
			}
			slog.Info("startup warm floor: restoring", "slug", prior.Slug)
			if done := w.driveWakingApp(ctx, prior.Slug, "startup"); done != nil {
				select {
				case <-done:
				case <-ctx.Done():
					return
				}
			}
		}
		pending = retry
		if len(pending) > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}
