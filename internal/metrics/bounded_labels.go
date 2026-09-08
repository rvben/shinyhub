package metrics

// A Prometheus CounterVec publishes nothing until a label combination is first
// used, so a freshly started server exposes no shinyhub_deploys_total series at
// all. That makes "no deploy has ever failed" and "this build does not record
// deploy failures" the same observation on the wire: an alert written as
// rate(shinyhub_deploys_total{result="failure"}[15m]) > 0 stays silent whether
// the system is healthy or the instrumentation is missing, and the operator
// finds out which during the incident it was supposed to catch.
//
// Seeding every value a counter can take publishes the series at zero from the
// first scrape, so absence of the metric means broken and zero means healthy.
//
// Only bounded label sets can be seeded. Anything keyed by slug, route or
// schedule is open-ended, and inventing series for values that have not
// happened would be fabricating data rather than declaring a known shape.
var (
	// deployResults are the outcomes recordDeploy passes.
	deployResults = []string{"success", "failure"}

	// generationHandoffOutcomes are the bounded outcomes of a version cutover.
	generationHandoffOutcomes = []string{
		"success",
		"deferred",
		"explicit_downtime",
		"candidate_failure",
		"cutover_repair",
		"forced_retirement",
		"cleanup_deferred",
	}

	// stateTransitionEvents are the lifecycle transitions the watcher records.
	stateTransitionEvents = []string{"hibernate", "sleep", "wake"}

	// usagePersistenceResults are the usage-recorder health signals.
	usagePersistenceResults = []string{
		"start_overflow",
		"start_retry",
		"start_failed",
		"start_dropped",
		"end_retry",
		"policy_refresh_failed",
	}

	// appLogFlushResults are the outcomes of a shared app-log flush attempt.
	appLogFlushResults = []string{"ok", "error"}

	// autoscaleDirections are the two ways the autoscaler can move a replica
	// count.
	autoscaleDirections = []string{"up", "down"}
)

// seedBoundedSeries publishes every bounded label combination at zero.
//
// Deliberately not seeded: the Fargate and external-provider-log counters,
// whose subsystems are absent on a server that does not use them. A series
// pinned at zero there would assert that an integration is running and quiet,
// when in fact it is not configured at all.
func seedBoundedSeries(r *Registry) {
	for _, v := range deployResults {
		r.deploys.WithLabelValues(v)
	}
	for _, v := range generationHandoffOutcomes {
		r.generationHandoffs.WithLabelValues(v)
	}
	for _, v := range stateTransitionEvents {
		r.stateTransitions.WithLabelValues(v)
	}
	for _, v := range usagePersistenceResults {
		r.usagePersistence.WithLabelValues(v)
	}
	for _, v := range appLogFlushResults {
		r.appLogFlushes.WithLabelValues(v)
	}
	for _, v := range autoscaleDirections {
		r.autoscaleScales.WithLabelValues(v)
	}
}

// boundedSeriesSpecs pairs each seeded metric with the label it is keyed by and
// the values seeded for it, so a test can check the published series against the
// same list the seeding uses without restating it.
func boundedSeriesSpecs() []struct {
	Metric string
	Label  string
	Values []string
} {
	return []struct {
		Metric string
		Label  string
		Values []string
	}{
		{"shinyhub_deploys_total", "result", deployResults},
		{"shinyhub_generation_handoffs_total", "outcome", generationHandoffOutcomes},
		{"shinyhub_app_state_transitions_total", "event", stateTransitionEvents},
		{"shinyhub_usage_persistence_events_total", "result", usagePersistenceResults},
		{"shinyhub_app_log_flush_attempts_total", "result", appLogFlushResults},
		{"shinyhub_autoscale_scale_total", "direction", autoscaleDirections},
	}
}
