package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// fleetHealthResponse is the aggregate, admin-only fleet overview returned by
// GET /api/fleet/health. It answers "is my fleet healthy across all backends?"
// in one call, and is generic enough to drive an external status page.
type fleetHealthResponse struct {
	ServerVersion     string                `json:"server_version"`
	Apps              fleetAppCounts        `json:"apps"`
	Replicas          fleetReplicaCounts    `json:"replicas"`
	Workers           *fleetWorkerCounts    `json:"workers,omitempty"`
	Tiers             []fleetHealthTier     `json:"tiers"`
	DegradedApps      []fleetHealthDegraded `json:"degraded_apps"`
	StaleSchedules    int                   `json:"stale_schedules"`
	StaleScheduleList []staleScheduleItem   `json:"stale_schedule_list"`
}

type staleScheduleItem struct {
	Slug     string `json:"slug"`
	Schedule string `json:"schedule"`
}

type fleetAppCounts struct {
	Total    int `json:"total"`
	Running  int `json:"running"`
	Stopped  int `json:"stopped"`
	Degraded int `json:"degraded"` // running apps with >=1 lost replica
	Crashed  int `json:"crashed"`  // apps down because their replicas cannot start
}

type fleetReplicaCounts struct {
	Running int `json:"running"`
	Lost    int `json:"lost"`
	Stopped int `json:"stopped"`
}

type fleetWorkerCounts struct {
	Total   int `json:"total"`
	Up      int `json:"up"`
	Down    int `json:"down"`
	Joining int `json:"joining"` // registered, not yet promoted by a first heartbeat
	Revoked int `json:"revoked"`
}

type fleetHealthTier struct {
	Tier            string `json:"tier"`
	Runtime         string `json:"runtime"`
	ReplicasRunning int    `json:"replicas_running"`
	ReplicasLost    int    `json:"replicas_lost"`
	WorkersUp       int    `json:"workers_up,omitempty"`
	WorkersDown     int    `json:"workers_down,omitempty"`
}

type fleetHealthDegraded struct {
	Slug   string `json:"slug"`
	Tier   string `json:"tier"`
	Lost   int    `json:"lost"`
	Reason string `json:"reason,omitempty"`
}

// maxDegradedApps caps the actionable degraded list so the response stays
// bounded on a large, unhealthy fleet.
const maxDegradedApps = 50

// handleFleetHealth returns aggregate fleet health across all apps and
// backends: app/replica/worker counts, a per-tier breakdown, and the bounded
// list of degraded apps. Admin only and side-effect free.
//
// A ?fleet=<id> scope is a planned follow-on; today the view is whole-server.
func (s *Server) handleFleetHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r); !ok {
		return
	}

	apps, err := s.store.ListApps(1_000_000, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp := fleetHealthResponse{
		ServerVersion: s.version,
		Apps:          fleetAppCounts{Total: len(apps)},
		Tiers:         []fleetHealthTier{},
		DegradedApps:  []fleetHealthDegraded{},
	}
	for _, a := range apps {
		switch a.Status {
		case "running":
			resp.Apps.Running++
		case "stopped":
			resp.Apps.Stopped++
		case "crashed":
			resp.Apps.Crashed++
		}
	}

	// Per-tier replica counts (summed across providers) + fleet totals.
	type tierAgg struct {
		running, lost   int
		workersUp, down int
	}
	tiers := map[string]*tierAgg{}
	at := func(t string) *tierAgg {
		if tiers[t] == nil {
			tiers[t] = &tierAgg{}
		}
		return tiers[t]
	}
	counts, err := s.store.FleetReplicaCounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	for _, c := range counts {
		switch c.Status {
		case db.ReplicaStatusRunning:
			resp.Replicas.Running += c.Count
			at(c.Tier).running += c.Count
		case db.ReplicaStatusLost:
			resp.Replicas.Lost += c.Count
			at(c.Tier).lost += c.Count
		case "stopped":
			resp.Replicas.Stopped += c.Count
		}
	}

	// Workers (omitted entirely when worker hosting is disabled).
	if s.workerReg != nil {
		if workers, werr := s.store.ListWorkers(); werr == nil {
			wc := &fleetWorkerCounts{Total: len(workers)}
			for _, wk := range workers {
				switch {
				case wk.Revoked():
					wc.Revoked++
				case wk.Status == "up":
					wc.Up++
					at(wk.Tier).workersUp++
				case wk.Status == "joining":
					// Transitional: registered but not yet promoted by its first
					// heartbeat. Not routable, but not a fault either - keep it out
					// of the down count so a just-joined worker does not read as a
					// degraded fleet.
					wc.Joining++
				default:
					wc.Down++
					at(wk.Tier).down++
				}
			}
			resp.Workers = wc
		}
	}

	// Degraded apps (apps with >=1 lost replica) + the actionable list.
	lost, err := s.store.AppsWithLostReplicas()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	degradedSlugs := map[string]bool{}
	for _, l := range lost {
		degradedSlugs[l.Slug] = true
		if len(resp.DegradedApps) < maxDegradedApps {
			resp.DegradedApps = append(resp.DegradedApps, fleetHealthDegraded{
				Slug:   l.Slug,
				Tier:   l.Tier,
				Lost:   l.Lost,
				Reason: s.lostReplicaReason(l.Tier),
			})
		}
	}
	resp.Apps.Degraded = len(degradedSlugs)

	// Emit tiers in a stable order: configured tiers first, then any extras.
	seen := map[string]bool{}
	emit := func(t string) {
		if t == "" || seen[t] || tiers[t] == nil {
			return
		}
		seen[t] = true
		ta := tiers[t]
		rt, _ := s.cfg.Runtime.RuntimeForTier(t)
		resp.Tiers = append(resp.Tiers, fleetHealthTier{
			Tier:            t,
			Runtime:         rt,
			ReplicasRunning: ta.running,
			ReplicasLost:    ta.lost,
			WorkersUp:       ta.workersUp,
			WorkersDown:     ta.down,
		})
	}
	for _, t := range s.cfg.Runtime.TierOrder() {
		emit(t)
	}
	extras := make([]string, 0, len(tiers))
	for t := range tiers {
		if !seen[t] {
			extras = append(extras, t)
		}
	}
	sort.Strings(extras)
	for _, t := range extras {
		emit(t)
	}

	// Stale schedules (cron-aware) for the admin banner. Same query the
	// metrics collector and `schedule status` use; bounded fleet, indexed.
	// The list is capped by maxDegradedApps (50): the same "actionable list"
	// bound used for degraded apps, so the response stays bounded on a large
	// unhealthy fleet. The count (StaleSchedules) is uncapped.
	resp.StaleScheduleList = []staleScheduleItem{}
	if frs, ferr := s.store.ScheduleFreshness(); ferr == nil {
		def := s.cfg.Scheduler.Location
		now := time.Now()
		for _, fr := range frs {
			if scheduleStale(fr, def, now) {
				resp.StaleSchedules++
				if len(resp.StaleScheduleList) < maxDegradedApps {
					resp.StaleScheduleList = append(resp.StaleScheduleList, staleScheduleItem{Slug: fr.Slug, Schedule: fr.Name})
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
