package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestDeploymentOriginForRequest(t *testing.T) {
	request := func(channel string) *http.Request {
		r := httptest.NewRequest("POST", "/api/apps/demo/deploy", nil)
		if channel != "" {
			r.Header.Set(deploymentChannelHeader, channel)
		}
		u := &auth.ContextUser{ID: 42, Username: "ruben", Role: "developer"}
		return r.WithContext(auth.WithUser(r.Context(), u))
	}

	tests := []struct {
		name, channel, runID, wantKind, wantChannel string
		rollback                                    bool
	}{
		{"dashboard", "dashboard", "", db.DeploymentOriginDirect, db.DeploymentChannelDashboard, false},
		{"cli", "cli", "", db.DeploymentOriginDirect, db.DeploymentChannelCLI, false},
		{"watch uses compatible cli channel", "watch", "", db.DeploymentOriginDirect, db.DeploymentChannelCLI, false},
		{"unknown client falls back to api", "forged", "", db.DeploymentOriginDirect, db.DeploymentChannelAPI, false},
		{"rollback keeps channel", "dashboard", "", db.DeploymentOriginRollback, db.DeploymentChannelDashboard, true},
		{"fleet run is authoritative", "dashboard", "0123456789abcdef0123456789abcdef", db.DeploymentOriginFleet, db.DeploymentChannelFleet, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := deploymentOriginForRequest(request(tc.channel), tc.runID, tc.rollback)
			if got.Kind != tc.wantKind || got.Channel != tc.wantChannel || got.Actor != "ruben" || got.UserID == nil || *got.UserID != 42 {
				t.Fatalf("origin = %#v", got)
			}
		})
	}
}

func TestDevelopmentRequestHeadersRequireCompleteWatchIdentity(t *testing.T) {
	valid := httptest.NewRequest("POST", "/api/apps/demo/deploy", nil)
	valid.Header.Set(deploymentChannelHeader, deploymentChannelWatch)
	valid.Header.Set(developmentSessionHeader, "0123456789abcdef0123456789abcdef")
	valid.Header.Set(developmentTargetHeader, db.DevelopmentTargetExisting)
	got, err := developmentRequestFromHeaders(valid)
	if err != nil || got.ID == "" || got.Target != db.DevelopmentTargetExisting {
		t.Fatalf("valid headers = %+v, err=%v", got, err)
	}

	incomplete := httptest.NewRequest("POST", "/api/apps/demo/deploy", nil)
	incomplete.Header.Set(deploymentChannelHeader, deploymentChannelWatch)
	if _, err := developmentRequestFromHeaders(incomplete); err == nil {
		t.Fatal("incomplete watch identity accepted")
	}

	forged := httptest.NewRequest("POST", "/api/apps/demo/deploy", nil)
	forged.Header.Set(developmentSessionHeader, "0123456789abcdef0123456789abcdef")
	forged.Header.Set(developmentTargetHeader, db.DevelopmentTargetExisting)
	if _, err := developmentRequestFromHeaders(forged); err == nil {
		t.Fatal("session identity accepted without watch channel")
	}
}
