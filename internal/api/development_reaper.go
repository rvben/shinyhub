package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

const (
	developmentAppReaperInterval   = time.Minute
	developmentSessionLeaseTimeout = 2 * time.Minute
)

// RunDevelopmentAppReaper removes expired ephemeral development apps while
// this server owns the control plane. Normal and --create watch targets are
// never present in ephemeral_apps and are therefore never considered.
func (s *Server) RunDevelopmentAppReaper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = developmentAppReaperInterval
	}
	s.reapExpiredDevelopmentApps(ctx, time.Now().UTC())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.reapExpiredDevelopmentApps(ctx, now.UTC())
		}
	}
}

func (s *Server) reapExpiredDevelopmentApps(ctx context.Context, now time.Time) {
	if ended, err := s.store.EndStaleDevelopmentSessions(developmentSessionLeaseTimeout); err != nil {
		slog.Error("end stale development sessions", "err", err)
	} else if ended > 0 {
		slog.Info("ended stale development sessions", "count", ended)
	}
	apps, err := s.store.ListExpiredEphemeralApps(now)
	if err != nil {
		slog.Error("list expired development apps", "err", err)
		return
	}
	for _, candidate := range apps {
		if ctx.Err() != nil {
			return
		}
		release := s.acquireDeployLock(candidate.Slug)
		app, err := s.store.GetAppBySlug(candidate.Slug)
		if errors.Is(err, db.ErrNotFound) {
			release()
			continue
		}
		if err != nil {
			release()
			slog.Error("load expired development app", "slug", candidate.Slug, "err", err)
			continue
		}
		if err := s.guardActivationLifecycle(app.ID, "expire "+candidate.Slug); err != nil {
			release()
			slog.Info("defer expired development app deletion", "slug", candidate.Slug, "reason", err)
			continue
		}
		detail, err := s.deleteAppLocked(ctx, app)
		release()
		if err != nil {
			slog.Error("expire development app", "slug", candidate.Slug, "err", err)
			continue
		}
		auditDetail, _ := json.Marshal(map[string]string{
			"development_session_id": candidate.SessionID,
			"expires_at":             candidate.ExpiresAt.UTC().Format(time.RFC3339Nano),
		})
		s.store.LogAuditEvent(db.AuditEventParams{
			Action:       "development_app_expired",
			ResourceType: "app",
			ResourceID:   candidate.Slug,
			Detail:       string(auditDetail),
		})
		slog.Info("expired development app removed", "slug", candidate.Slug, "session", candidate.SessionID, "expires_at", candidate.ExpiresAt, "detail", detail)
	}
}
