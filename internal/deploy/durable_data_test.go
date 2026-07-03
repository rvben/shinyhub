package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

// UsesPersistentData is the app-side signal for the durable-data guard: an app
// uses persistent data if its command references {data_dir} OR it already has
// data on disk under appDataDir/slug.

func TestUsesPersistentData_CommandReferencesDataDir(t *testing.T) {
	cmd := []string{"uv", "run", "shiny", "run", "--port", "{port}", "app.py", "--data", "{data_dir}"}
	got, err := UsesPersistentData(cmd, "", "myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("command references {data_dir}: want true, got false")
	}
}

func TestUsesPersistentData_NoDataDirNoDisk(t *testing.T) {
	cmd := []string{"uv", "run", "shiny", "run", "--port", "{port}", "app.py"}
	got, err := UsesPersistentData(cmd, "", "myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("stateless command, no appDataDir: want false, got true")
	}
}

func TestUsesPersistentData_DataOnDisk(t *testing.T) {
	appDataDir := t.TempDir()
	slug := "myapp"
	if err := os.MkdirAll(filepath.Join(appDataDir, slug), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDataDir, slug, "state.csv"), []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := []string{"uv", "run", "shiny", "run", "--port", "{port}", "app.py"}
	got, err := UsesPersistentData(cmd, appDataDir, slug)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("data present on disk: want true, got false")
	}
}

func TestUsesPersistentData_EmptyDataDirOnDisk(t *testing.T) {
	appDataDir := t.TempDir() // no slug subdir written
	cmd := []string{"uv", "run", "shiny", "run", "--port", "{port}", "app.py"}
	got, err := UsesPersistentData(cmd, appDataDir, "myapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("no data on disk: want false, got true")
	}
}

// EphemeralDataBlockedTier decides whether a deploy must be blocked: a data-using
// app (usesData) with no operator acknowledgement (ack) may not land on any tier
// whose storage is not durable. Fail-closed across mixed placement.

func allDurable(string) bool  { return true }
func noneDurable(string) bool { return false }

func TestEphemeralDataBlockedTier_StatelessAllowed(t *testing.T) {
	if tier, blocked := EphemeralDataBlockedTier(false, false, []string{"cloud"}, noneDurable); blocked {
		t.Fatalf("stateless app: want allowed, got blocked on %q", tier)
	}
}

func TestEphemeralDataBlockedTier_AckAllowed(t *testing.T) {
	if tier, blocked := EphemeralDataBlockedTier(true, true, []string{"cloud"}, noneDurable); blocked {
		t.Fatalf("acknowledged app: want allowed, got blocked on %q", tier)
	}
}

func TestEphemeralDataBlockedTier_DurableAllowed(t *testing.T) {
	if tier, blocked := EphemeralDataBlockedTier(true, false, []string{"cloud", "local"}, allDurable); blocked {
		t.Fatalf("all tiers durable: want allowed, got blocked on %q", tier)
	}
}

func TestEphemeralDataBlockedTier_EphemeralBlocked(t *testing.T) {
	tier, blocked := EphemeralDataBlockedTier(true, false, []string{"cloud"}, noneDurable)
	if !blocked {
		t.Fatal("data-using app on ephemeral tier: want blocked, got allowed")
	}
	if tier != "cloud" {
		t.Fatalf("blocked tier = %q, want cloud", tier)
	}
}

func TestEphemeralDataBlockedTier_MixedFailsClosed(t *testing.T) {
	// One durable tier (local) and one ephemeral (cloud): must block, naming the
	// ephemeral tier, rather than deploying only the durable replicas.
	durable := func(tier string) bool { return tier == "local" }
	tier, blocked := EphemeralDataBlockedTier(true, false, []string{"local", "cloud"}, durable)
	if !blocked {
		t.Fatal("mixed placement with an ephemeral tier: want blocked, got allowed")
	}
	if tier != "cloud" {
		t.Fatalf("blocked tier = %q, want cloud (the ephemeral one)", tier)
	}
}
