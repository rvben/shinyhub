package process

import (
	"strings"
	"testing"
)

func TestFilteredEnvStripsShinyHubVars(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "super-secret")
	t.Setenv("SHINYHUB_GITHUB_CLIENT_SECRET", "github-secret")
	t.Setenv("SHINYHUB_OIDC_CLIENT_SECRET", "oidc-secret")
	t.Setenv("PATH", "/usr/bin:/bin") // should be preserved

	env := filteredEnv()

	for _, e := range env {
		if strings.HasPrefix(e, "SHINYHUB_") {
			t.Errorf("SHINYHUB_ var leaked into child env: %s", e)
		}
	}

	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			found = true
		}
	}
	if !found {
		t.Error("PATH was unexpectedly stripped from child env")
	}
}

func TestFilteredEnvPreservesNonShinyHubVars(t *testing.T) {
	t.Setenv("SHINYHUB_AUTH_SECRET", "should-be-stripped")
	t.Setenv("MY_APP_SECRET", "should-be-kept")

	env := filteredEnv()

	keptFound := false
	for _, e := range env {
		if e == "MY_APP_SECRET=should-be-kept" {
			keptFound = true
		}
		if strings.HasPrefix(e, "SHINYHUB_") {
			t.Errorf("SHINYHUB_ var present in filtered env: %s", e)
		}
	}
	if !keptFound {
		t.Error("expected MY_APP_SECRET to be preserved in filtered env")
	}
}
