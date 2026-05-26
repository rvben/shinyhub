package deploy

import "testing"

func TestCheckColocatedShared(t *testing.T) {
	nodeForTier := func(tier string) string {
		switch tier {
		case "local":
			return "cp"
		case "remote":
			return "node-a"
		}
		return ""
	}

	// Consumer on remote mounts a source that runs on local (cp): cross-node -> error.
	err := CheckColocatedShared(
		[]string{"remote"},
		map[string][]string{"shared": {"local"}},
		nodeForTier,
	)
	if err == nil {
		t.Fatal("cross-node shared mount: want error, got nil")
	}

	// Consumer and source both on node-a: ok.
	err = CheckColocatedShared(
		[]string{"remote"},
		map[string][]string{"shared": {"remote"}},
		nodeForTier,
	)
	if err != nil {
		t.Errorf("co-located shared mount: want nil, got %v", err)
	}

	// Consumer spans two tiers on different nodes; source only on one -> error.
	err = CheckColocatedShared(
		[]string{"local", "remote"},
		map[string][]string{"shared": {"local"}},
		nodeForTier,
	)
	if err == nil {
		t.Error("consumer tier without co-located source: want error")
	}
}
