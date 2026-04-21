package api

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

// TestAppsAPI_ConcurrentRedeployDedup verifies that tryAcquireRedeploy/releaseRedeploy
// prevent two concurrent redeployApp goroutines from running for the same slug.
func TestAppsAPI_ConcurrentRedeployDedup(t *testing.T) {
	s := &Server{cfg: &config.Config{}}

	const slug = "myapp"

	// First acquire must succeed.
	if !s.tryAcquireRedeploy(slug) {
		t.Fatal("first tryAcquireRedeploy should return true")
	}

	// Second acquire while first is held must be rejected.
	if s.tryAcquireRedeploy(slug) {
		t.Fatal("second tryAcquireRedeploy should return false while first is held")
	}

	// After release, a new acquire must succeed again.
	s.releaseRedeploy(slug)
	if !s.tryAcquireRedeploy(slug) {
		t.Fatal("tryAcquireRedeploy after release should return true")
	}
	s.releaseRedeploy(slug)
}
