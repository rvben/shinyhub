package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestDeploymentReadPathsExposeContentDigest(t *testing.T) {
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "owner", "developer")
	app := mustCreateApp(t, store, "demo", owner.ID)

	dep, err := store.BeginDeployment(app.ID, "v1", "/bundles/demo/v1")
	if err != nil {
		t.Fatalf("begin deployment: %v", err)
	}
	const digest = "sha256:abc123"
	if err := store.SetDeploymentDigest(dep.ID, digest); err != nil {
		t.Fatalf("set digest: %v", err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatalf("promote: %v", err)
	}

	list, err := store.ListDeployments(app.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ContentDigest != digest {
		t.Fatalf("ListDeployments digest = %q, want %q", list[0].ContentDigest, digest)
	}

	got, err := store.GetDeploymentByDigest(digest)
	if err != nil {
		t.Fatalf("get by digest: %v", err)
	}
	if got.ID != dep.ID || got.AppID != app.ID {
		t.Fatalf("GetDeploymentByDigest = %+v, want id=%d app=%d", got, dep.ID, app.ID)
	}

	single, err := store.GetDeploymentBySlugAndID("demo", dep.ID)
	if err != nil {
		t.Fatalf("get by slug+id: %v", err)
	}
	if single.ContentDigest != digest {
		t.Fatalf("GetDeploymentBySlugAndID digest = %q, want %q", single.ContentDigest, digest)
	}

	// GetDeploymentByDigest must return ErrNotFound for an unknown digest.
	_, err = store.GetDeploymentByDigest("sha256:notexist")
	if !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("GetDeploymentByDigest unknown digest: got %v, want ErrNotFound", err)
	}
}
