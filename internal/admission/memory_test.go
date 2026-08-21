package admission

import "testing"

const (
	gib = uint64(1) << 30
	mib = uint64(1) << 20
)

func TestResolveMemoryPrefersConfigOverride(t *testing.T) {
	// A positive override wins over everything and is reported as "config".
	mb, src, ok := resolveMemory(2048, 4*gib, true, 16*gib, true)
	if mb != 2048 || src != "config" || !ok {
		t.Fatalf("resolveMemory(override=2048) = (%v, %q, %v), want (2048, config, true)", mb, src, ok)
	}
}

func TestResolveMemoryUsesCgroupWhenItBinds(t *testing.T) {
	// No override. The cgroup limit (4 GiB) is below the host total (16 GiB),
	// so the cgroup binds and is the reported source.
	mb, src, ok := resolveMemory(0, 4*gib, true, 16*gib, true)
	if mb != 4096 || src != "cgroup-limit" || !ok {
		t.Fatalf("resolveMemory = (%v, %q, %v), want (4096, cgroup-limit, true)", mb, src, ok)
	}
}

func TestResolveMemoryUsesHostTotalWhenCgroupIsLooser(t *testing.T) {
	// A cgroup limit above the host total describes a ceiling the host cannot
	// reach, so the host total binds.
	mb, src, ok := resolveMemory(0, 32*gib, true, 16*gib, true)
	if mb != 16384 || src != "host-total" || !ok {
		t.Fatalf("resolveMemory = (%v, %q, %v), want (16384, host-total, true)", mb, src, ok)
	}
}

func TestResolveMemoryHostTotalWhenNoCgroup(t *testing.T) {
	// No cgroup limit (macOS, or an uncapped container): the host total is the
	// answer, not a degraded one.
	mb, src, ok := resolveMemory(0, 0, false, 8*gib, true)
	if mb != 8192 || src != "host-total" || !ok {
		t.Fatalf("resolveMemory = (%v, %q, %v), want (8192, host-total, true)", mb, src, ok)
	}
}

func TestResolveMemoryCgroupOnlyWhenHostTotalUnreadable(t *testing.T) {
	// The host total failing to read must not suppress a cgroup limit that did.
	mb, src, ok := resolveMemory(0, 512*mib, true, 0, false)
	if mb != 512 || src != "cgroup-limit" || !ok {
		t.Fatalf("resolveMemory = (%v, %q, %v), want (512, cgroup-limit, true)", mb, src, ok)
	}
}

func TestResolveMemoryUnknownWhenNothingReadable(t *testing.T) {
	// Neither source answered. This must report unknown rather than a host
	// with zero memory, which would render as a full meter over no capacity.
	mb, src, ok := resolveMemory(0, 0, false, 0, false)
	if ok || mb != 0 || src != "" {
		t.Fatalf("resolveMemory(nothing) = (%v, %q, %v), want (0, \"\", false)", mb, src, ok)
	}
}

func TestResolveMemoryNonPositiveOverrideIgnored(t *testing.T) {
	// Zero override means "autodetect", not "zero memory".
	mb, src, ok := resolveMemory(0, 0, false, 2*gib, true)
	if mb != 2048 || src != "host-total" || !ok {
		t.Fatalf("resolveMemory(override=0) = (%v, %q, %v), want (2048, host-total, true)", mb, src, ok)
	}
}

func TestResolveMemoryRoundsSubMegabyteUp(t *testing.T) {
	// A capacity that was genuinely read must never report 0 MiB, because 0 is
	// how this function spells "unknown" everywhere else.
	mb, src, ok := resolveMemory(0, 0, false, 4096, true)
	if mb != 1 || src != "host-total" || !ok {
		t.Fatalf("resolveMemory(4096 bytes) = (%v, %q, %v), want (1, host-total, true)", mb, src, ok)
	}
}

func TestDetectMemoryReturnsUsableCapacity(t *testing.T) {
	// Integration smoke: on the test host DetectMemory must find a real figure.
	mb, src, ok := DetectMemory(0)
	if !ok {
		t.Fatal("DetectMemory reported no capacity on a host that has memory")
	}
	if mb < 1 {
		t.Fatalf("DetectMemory mb = %v, want >= 1", mb)
	}
	switch src {
	case "config", "cgroup-limit", "host-total":
	default:
		t.Fatalf("DetectMemory source = %q, want one of config/cgroup-limit/host-total", src)
	}
}
