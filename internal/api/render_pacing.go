package api

import "github.com/rvben/shinyhub/internal/admission"

// renderPacingBlock is the advisory pacing summary returned when an app has
// render_seconds > 0: the suggested session cap for steady-state protection, the
// app's current effective cap, and the assumptions behind the number.
type renderPacingBlock struct {
	RenderSeconds                         float64 `json:"render_seconds"`
	EffectiveCores                        float64 `json:"effective_cores"`
	CoresSource                           string  `json:"cores_source"`
	SuggestedMaxSessionsPerReplica        int     `json:"suggested_max_sessions_per_replica"`
	CurrentEffectiveMaxSessionsPerReplica int     `json:"current_effective_max_sessions_per_replica"`
	CadenceAssumptionSeconds              float64 `json:"cadence_assumption_seconds"`
}

// buildRenderPacingBlock returns the advisory block for a paced app, or nil when
// pacing is off (renderSeconds <= 0). effectiveCap is the app's resolved
// max_sessions_per_replica (its own value, or the runtime default when 0).
func (s *Server) buildRenderPacingBlock(renderSeconds float64, effectiveCap int) *renderPacingBlock {
	if renderSeconds <= 0 {
		return nil
	}
	suggested, cadence := admission.SuggestSessionCap(s.renderPacingCores, s.cfg.RenderHeadroom(), renderSeconds)
	return &renderPacingBlock{
		RenderSeconds:                         renderSeconds,
		EffectiveCores:                        s.renderPacingCores,
		CoresSource:                           s.renderPacingSource,
		SuggestedMaxSessionsPerReplica:        suggested,
		CurrentEffectiveMaxSessionsPerReplica: effectiveCap,
		CadenceAssumptionSeconds:              cadence,
	}
}
