package main

import (
	"os"
	"strings"
	"testing"
)

// The replica boot function handed to the watcher lives in main.go, which cannot
// be unit imported. It serves every on-demand boot: a wake from hibernation and
// a crash restart. Without a preparation mode it defaults to a promotion and
// re-runs the full dependency build (`uv sync` / `renv::restore`) before each
// boot, which is most of an R app's wake time. deploy.RunReplica's handling of
// the mode is tested in internal/deploy; this pins that the boot function
// actually sets it, from the serving deployment it boots, before it boots.
func TestReplicaDeployFnActivatesPreparedDeployments(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)

	const (
		open    = "deployFn := func(ctx context.Context, slug, bundleDir string, index int)"
		current = "current, derr := store.GetServingDeployment(app.ID)"
		prep    = "p.Preparation = deploy.ActivationPreparation(current.Prepared)"
		boot    = "return deploy.RunReplica(traceReplica(ctx, tracer, p), index)"
	)
	start := strings.Index(s, open)
	if start < 0 {
		t.Fatal("replica deploy function not found in main.go")
	}
	s = s[start:]
	end := strings.Index(s, boot)
	if end < 0 {
		t.Fatal("replica boot not found inside deployFn")
	}
	// Other lifecycle closures also load the serving deployment. Check only
	// the replica deploy function's wiring, through its RunReplica call.
	s = s[:end+len(boot)]
	idx := map[string]int{}
	for _, needle := range []string{open, current, prep, boot} {
		if n := strings.Count(s, needle); n != 1 {
			t.Fatalf("%q appears %d times in main.go, want 1; the wiring this test pins has moved or gone", needle, n)
		}
		idx[needle] = strings.Index(s, needle)
	}
	if !(idx[open] < idx[current] && idx[current] < idx[prep] && idx[prep] < idx[boot]) {
		t.Fatalf("the preparation mode must be set inside deployFn, after the current deployment is loaded and before RunReplica; offsets open=%d current=%d prep=%d boot=%d",
			idx[open], idx[current], idx[prep], idx[boot])
	}
}
