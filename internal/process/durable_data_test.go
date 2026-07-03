package process

import "testing"

// ephemeralTierRuntime implements the optional DurableDataReporter capability
// and reports its storage as ephemeral (like bare Fargate).
type ephemeralTierRuntime struct{ stubTierRuntime }

func (ephemeralTierRuntime) TierHasDurableData() bool { return false }

// TierHasDurableDataFor defaults to true for a runtime that does not implement
// DurableDataReporter (native/docker/remote all have host-backed durable dirs).
func TestTierHasDurableDataFor_DefaultsTrue(t *testing.T) {
	m := NewManager(t.TempDir(), &stubTierRuntime{})
	if !m.TierHasDurableDataFor(DefaultTier) {
		t.Fatal("runtime without DurableDataReporter: want durable=true, got false")
	}
}

// A runtime that reports ephemeral storage propagates through the proxy.
func TestTierHasDurableDataFor_ReportsEphemeral(t *testing.T) {
	m := NewManager(t.TempDir(), &stubTierRuntime{})
	m.RegisterRuntime("cloud", ephemeralTierRuntime{})
	if m.TierHasDurableDataFor("cloud") {
		t.Fatal("ephemeral runtime: want durable=false, got true")
	}
	// Unknown tier falls back to the default runtime (durable).
	if !m.TierHasDurableDataFor("nonexistent") {
		t.Fatal("unknown tier falls back to default: want durable=true, got false")
	}
}
