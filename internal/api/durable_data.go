package api

import (
	"fmt"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// ephemeralDataDeployBlock reports the tier a data-using app would land on with
// ephemeral (task-local, restart-losing, replica-unshared) storage, and true,
// or "" and false when the deploy is allowed. It wires the durable-data guard:
// the app-side signal (command template + already-pushed data), the operator's
// ephemeral_data_ack, the app's placement tiers, and each tier's durability as
// reported by its runtime. A nil manager (servers built without one) never
// blocks. Fail-closed across mixed placement.
func (s *Server) ephemeralDataDeployBlock(app *db.App, command []string) (string, bool, error) {
	return s.ephemeralDataBlockForTiers(app, command, s.tiersForApp(app))
}

// ephemeralDataBlockForTiers is the guard evaluated against an explicit tier set.
// Deploy paths pass the app's current tiers; a placement CHANGE passes the
// proposed new tiers so the move is rejected before it is persisted (otherwise
// `apps set --tier <ephemeral>` and the async redeploy it triggers would move a
// data-using app onto ephemeral storage unguarded). A nil manager never blocks.
func (s *Server) ephemeralDataBlockForTiers(app *db.App, command []string, tiers []string) (string, bool, error) {
	if s.manager == nil {
		return "", false, nil
	}
	uses, err := deploy.UsesPersistentData(command, s.cfg.Storage.AppDataDir, app.Slug)
	if err != nil {
		return "", false, err
	}
	tier, blocked := deploy.EphemeralDataBlockedTier(uses, app.EphemeralDataAck, tiers, s.manager.TierHasDurableDataFor)
	return tier, blocked, nil
}

// ephemeralDataPushBlock reports whether pushing data to app must be blocked
// because a placement tier has no durable backend. Pushing data is itself the
// app-side signal, so usesData is unconditionally true here. A nil manager never
// blocks.
func (s *Server) ephemeralDataPushBlock(app *db.App) (string, bool) {
	if s.manager == nil {
		return "", false
	}
	return deploy.EphemeralDataBlockedTier(true, app.EphemeralDataAck, s.tiersForApp(app), s.manager.TierHasDurableDataFor)
}

// manifestCommand returns the command template declared in the manifest, or nil.
func manifestCommand(m *deploy.Manifest) []string {
	if m == nil {
		return nil
	}
	return m.App.Command
}

// ephemeralDataDeployMsg is the 422 body for a blocked deploy: it names the
// offending tier, states the concrete consequence, and gives the three exits.
func ephemeralDataDeployMsg(tier string) string {
	return fmt.Sprintf("this app writes persistent data ({data_dir} in its command, or data already pushed), "+
		"but tier %q has no durable storage backend. On Fargate, task storage is ephemeral: data is lost on "+
		"restart/hibernation and is not shared across replicas. Configure a durable backend "+
		"(runtime.fargate.s3files, or runtime.fargate.durable_data: true if you attached a volume in the task "+
		"definition), deploy to a tier that persists data, or accept ephemeral storage with --ephemeral-data-ok.", tier)
}

// ephemeralDataPushMsg is the 422 body for a blocked data push.
func ephemeralDataPushMsg(slug, tier string) string {
	return fmt.Sprintf("cannot push data to %q: tier %q has no durable storage backend, so pushed data would be "+
		"lost on restart/hibernation and not shared across replicas. Configure runtime.fargate.s3files (or "+
		"runtime.fargate.durable_data: true for a manually attached volume), or accept ephemeral storage on this "+
		"app with --ephemeral-data-ok.", slug, tier)
}
