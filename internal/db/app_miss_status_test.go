package db

import "testing"

// MissStatus is what the proxy's status lookup reports on a request for an
// app with no live backend. A pending deployment row means a deploy is in
// flight, so it overrides the stored status, which is stale during the
// window: "stopped" for a first deploy, "running" for a redeploy, "crashed"
// when a fix for a crashed app is being deployed. For the terminal statuses
// (stopped/crashed) the row is trusted only while the caller reports the
// app's deploy lock held, so a stale pending row (a PromoteDeployment
// failure) cannot mask a later stop or crash behind an unbounded deploying
// page.
func TestAppMissStatus(t *testing.T) {
	cases := []struct {
		name           string
		app            App
		deployInFlight bool
		wantStatus     string
		wantReason     string
	}{
		{
			name:           "pending overrides stopped while the deploy executes (first deploy in flight)",
			app:            App{Status: "stopped", LastDeploymentStatus: DeploymentPending},
			deployInFlight: true,
			wantStatus:     "deploying",
		},
		{
			name:           "pending overrides crashed while the fix deploy executes",
			app:            App{Status: "crashed", LastError: "boom", LastDeploymentStatus: DeploymentPending},
			deployInFlight: true,
			wantStatus:     "deploying",
		},
		{
			name:       "pending overrides running row-only (redeploy window; standby-safe)",
			app:        App{Status: "running", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deploying",
		},
		{
			name:       "pending overrides hibernated row-only (wake trigger still fires)",
			app:        App{Status: "hibernated", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deploying",
		},
		{
			name:       "stale pending next to stopped shows the stopped page",
			app:        App{Status: "stopped", LastDeploymentStatus: DeploymentPending},
			wantStatus: "stopped",
		},
		{
			name:       "stale pending next to crashed shows the crash page with its reason",
			app:        App{Status: "crashed", LastError: "boom", LastDeploymentStatus: DeploymentPending},
			wantStatus: "crashed",
			wantReason: "boom",
		},
		{
			name:       "failed deploy passes the stored status through",
			app:        App{Status: "stopped", LastDeploymentStatus: DeploymentFailed},
			wantStatus: "stopped",
		},
		{
			name:       "succeeded passes through with the crash reason",
			app:        App{Status: "crashed", LastError: "boom", LastDeploymentStatus: DeploymentSucceeded},
			wantStatus: "crashed",
			wantReason: "boom",
		},
		{
			name:       "never deployed passes through",
			app:        App{Status: "stopped"},
			wantStatus: "stopped",
		},
		{
			name:       "pending overrides waking row-only (wake mid-flight)",
			app:        App{Status: "waking", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deploying",
		},
		{
			name:       "pending overrides degraded row-only (watchdog reconciles)",
			app:        App{Status: "degraded", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deploying",
		},
		{
			name:       "pending never overrides a deleting tombstone",
			app:        App{Status: "deleting", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deleting",
		},
		{
			name:           "deleting stays deleting even while the delete holds the deploy lock",
			app:            App{Status: "deleting", LastDeploymentStatus: DeploymentPending},
			deployInFlight: true,
			wantStatus:     "deleting",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reason := tc.app.MissStatus(tc.deployInFlight)
			if status != tc.wantStatus || reason != tc.wantReason {
				t.Errorf("MissStatus(%v) = (%q, %q), want (%q, %q)",
					tc.deployInFlight, status, reason, tc.wantStatus, tc.wantReason)
			}
		})
	}
}
