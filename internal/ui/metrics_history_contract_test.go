package ui_test

import "testing"

// TestTrendsCardConsumesHistorySeries guards the API/frontend contract for
// GET /api/apps/:slug/metrics/history. The server returns a columnar payload
// {window_seconds, interval_seconds, series:{ts,cpu,rss,sessions,instances}}
// (see internal/api/metrics_history.go handleMetricsHistory + internal/history
// Series). trends-card.js must read those exact keys; a rename on either side
// would silently empty the Trends card otherwise.
func TestTrendsCardConsumesHistorySeries(t *testing.T) {
	const contract = "GET /api/apps/:slug/metrics/history returns {window_seconds, interval_seconds, series:{ts,cpu,rss,sessions,instances}}; see internal/api/metrics_history.go"
	assertContains(t, "views/trends-card.js", "window_seconds", contract)
	assertContains(t, "views/trends-card.js", "series.cpu", contract)
	assertContains(t, "views/trends-card.js", "series.rss", contract)
	assertContains(t, "views/trends-card.js", "series.sessions", contract)
	assertContains(t, "views/trends-card.js", "series.instances", contract)
}

// TestOverviewWiresTrendsCard guards that the Overview tab actually fetches the
// history endpoint and renders the Trends card. The rendering logic lives in the
// jsdom-tested trends-card.js module; this pins the wiring inside app-detail.js
// (which jsdom cannot import) so the card can't be silently dropped.
func TestOverviewWiresTrendsCard(t *testing.T) {
	assertContains(t, "views/app-detail.js", "/static/views/trends-card.js",
		"app-detail.js must import the Trends card renderer")
	assertContains(t, "views/app-detail.js", "renderTrendsCard",
		"app-detail.js must call renderTrendsCard to populate the Trends card")
	assertContains(t, "views/app-detail.js", "/metrics/history",
		"app-detail.js must fetch GET /api/apps/:slug/metrics/history")
	assertContains(t, "views/app-detail.js", `id="overview-trends"`,
		"the Overview panel must keep the #overview-trends container the Trends card renders into")
}
