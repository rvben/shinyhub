package db

import "testing"

// MissStatus is what the proxy's status lookup reports on a request for an
// app with no live backend. A pending deployment row means a deploy is in
// flight right now (the deploy tears the pool down before the new one boots),
// so it must take precedence over the stored status, which is stale during
// the window: "stopped" for a first deploy, "running" for a redeploy,
// "crashed" when a fix for a crashed app is being deployed.
func TestAppMissStatus(t *testing.T) {
	cases := []struct {
		name       string
		app        App
		wantStatus string
		wantReason string
	}{
		{
			name:       "pending overrides stopped (first deploy in flight)",
			app:        App{Status: "stopped", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deploying",
		},
		{
			name:       "pending overrides running (redeploy in flight)",
			app:        App{Status: "running", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deploying",
		},
		{
			name:       "pending overrides crashed and drops the stale reason",
			app:        App{Status: "crashed", LastError: "boom", LastDeploymentStatus: DeploymentPending},
			wantStatus: "deploying",
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reason := tc.app.MissStatus()
			if status != tc.wantStatus || reason != tc.wantReason {
				t.Errorf("MissStatus() = (%q, %q), want (%q, %q)",
					status, reason, tc.wantStatus, tc.wantReason)
			}
		})
	}
}
