package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

func TestMatchInventoryItem(t *testing.T) {
	items := []process.InventoryItem{
		{ContainerID: "c-1", Running: true, URL: "https://w:8443/v1/data/tok",
			Labels: map[string]string{
				"shinyhub.slug": "app", "shinyhub.replica_index": "0",
				"shinyhub.deployment_id": "7",
			}},
	}

	// Current deployment: matches.
	got := matchInventoryItem(items, "app", 0, "7")
	if got == nil {
		t.Fatal("current-deployment replica not matched")
	}
	if got.URL != "https://w:8443/v1/data/tok" {
		t.Errorf("URL = %q", got.URL)
	}
	// Superseded deployment (same slug+index, different deployment) must NOT match.
	if stale := matchInventoryItem(items, "app", 0, "9"); stale != nil {
		t.Errorf("stale-deployment container was matched: %+v", stale)
	}
	// Wrong index does not match.
	if mismatch := matchInventoryItem(items, "app", 1, "7"); mismatch != nil {
		t.Errorf("wrong-index match: %+v", mismatch)
	}
	// Empty deploymentID (legacy replica row) matches on slug+index alone.
	if legacy := matchInventoryItem(items, "app", 0, ""); legacy == nil {
		t.Error("legacy empty-deployment replica should match on slug+index")
	}
}
