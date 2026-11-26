package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/jobs"
)

// checkCandidateProducerEnvironments builds the uv environment of every
// candidate producer of deployment once its bundle is prepared and before the
// operation crosses its producer barrier. Past the barrier a failed producer
// quarantines the app, so a dependency set uv cannot install has to fail the
// operation here, where the failure path still restores or keeps the
// previous release: a producer that runs a bundle-declared command (an
// explicit `[app] command`, or any producer of an R app) gets no environment
// from the bundle's own preparation, so nothing else would notice it.
//
// Each outcome is reported to event as it is known. It returns the warnings
// for producers that could not be checked, or the error to fail the
// operation with.
func (s *Server) checkCandidateProducerEnvironments(ctx context.Context, producers []*db.Schedule, app *db.App, deployment *db.Deployment, event func(deployevent.Event)) ([]string, error) {
	// Like the deploy's own build, the check outlives the request: a client
	// that disconnects must not fail a candidate that is building well. Each
	// check still keeps the bundle's build budget.
	ctx = context.WithoutCancel(ctx)
	var warnings []string
	for _, producer := range producers {
		check, err := s.jobs.CheckCandidateProducerEnvironment(ctx, producer, app, deployment)
		var envErr *jobs.ProducerEnvironmentError
		switch {
		case errors.As(err, &envErr):
			slog.Warn("deploy: producer environment cannot be built", "slug", app.Slug, "schedule", producer.Name, "err", err)
			event(deployevent.Phase("producer_environment", deployevent.StatusFailed,
				fmt.Sprintf("Environment of producer %q cannot be built", producer.Name)))
			return nil, fmt.Errorf("deploy-triggered %w", err)
		case err != nil:
			slog.Error("deploy: check producer environment", "slug", app.Slug, "schedule", producer.Name, "err", err)
			return nil, fmt.Errorf("check the environment of deploy-triggered schedule %q: %w", producer.Name, err)
		case check.Checked:
			event(deployevent.Phase("producer_environment", deployevent.StatusCompleted,
				fmt.Sprintf("Environment of producer %q built", producer.Name)))
		case check.NotChecked != "":
			slog.Warn("deploy: producer environment not checked", "slug", app.Slug, "schedule", producer.Name, "reason", check.NotChecked)
			event(deployevent.Phase("producer_environment", deployevent.StatusWarning,
				fmt.Sprintf("Environment of producer %q not checked: %s", producer.Name, check.NotChecked)))
			warnings = append(warnings, fmt.Sprintf("schedule %q: environment not checked before the deploy: %s", producer.Name, check.NotChecked))
		}
	}
	return warnings, nil
}
