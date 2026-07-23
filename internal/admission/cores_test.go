package admission

import "testing"

func TestResolvePrefersConfigOverride(t *testing.T) {
	// A positive override wins over everything and is reported as "config".
	cores, src := resolve(4, 8, 2, true)
	if cores != 4 || src != "config" {
		t.Fatalf("resolve(override=4) = (%v, %q), want (4, config)", cores, src)
	}
}

func TestResolveUsesQuotaWhenItBinds(t *testing.T) {
	// No override. Quota (2) is below GOMAXPROCS (8), so the quota binds.
	cores, src := resolve(0, 8, 2, true)
	if cores != 2 || src != "cgroup-quota" {
		t.Fatalf("resolve = (%v, %q), want (2, cgroup-quota)", cores, src)
	}
}

func TestResolveUsesAffinityWhenQuotaIsLooser(t *testing.T) {
	// Quota (16) is above GOMAXPROCS (8), so affinity binds and is the source.
	cores, src := resolve(0, 8, 16, true)
	if cores != 8 || src != "affinity" {
		t.Fatalf("resolve = (%v, %q), want (8, affinity)", cores, src)
	}
}

func TestResolveAffinityWhenNoQuota(t *testing.T) {
	// No quota available (e.g. macOS, or no cgroup limit): affinity is the answer.
	cores, src := resolve(0, 8, 0, false)
	if cores != 8 || src != "affinity" {
		t.Fatalf("resolve = (%v, %q), want (8, affinity)", cores, src)
	}
}

func TestResolveNonPositiveOverrideIgnored(t *testing.T) {
	// Zero override means "autodetect", not "zero cores".
	cores, src := resolve(0, 4, 0, false)
	if cores != 4 || src != "affinity" {
		t.Fatalf("resolve(override=0) = (%v, %q), want (4, affinity)", cores, src)
	}
}

func TestDetectReturnsPositiveCores(t *testing.T) {
	// Integration smoke: on the test host Detect must return a usable value.
	cores, src := Detect(0)
	if cores < 1 {
		t.Fatalf("Detect cores = %v, want >= 1", cores)
	}
	switch src {
	case "config", "cgroup-quota", "affinity":
	default:
		t.Fatalf("Detect source = %q, want one of config/cgroup-quota/affinity", src)
	}
}
