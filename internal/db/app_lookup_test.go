package db_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestAppLookupRecoversAfterPreparationFailure(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	st, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.GetAppBySlug("missing"); err == nil {
		t.Fatal("lookup before migration should fail")
	}
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAppBySlug("missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("lookup after migration: %v", err)
	}
}

func TestAppLookupPreparedStatementConcurrentAndFresh(t *testing.T) {
	// Use several real connections, not the single-connection in-memory pool.
	dbtest.SkipIfPostgres(t)
	path := filepath.Join(t.TempDir(), "lookup.db")
	dbtest.WriteSQLiteFile(t, path)
	st, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := st.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two"} {
		if _, err := st.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: owner.ID, Access: "public"}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			slug := []string{"one", "two"}[worker%2]
			for n := 0; n < 20; n++ {
				app, err := st.GetAppBySlug(slug)
				if err != nil {
					t.Error(err)
					return
				}
				if app.Slug != slug || app.Name != slug || app.OwnerID != owner.ID {
					t.Errorf("wrong app for %s: %+v", slug, app)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := st.SetAppAccess("one", "private"); err != nil {
		t.Fatal(err)
	}
	app, err := st.GetAppBySlug("one")
	if err != nil {
		t.Fatal(err)
	}
	if app.Access != "private" {
		t.Fatal("cached a stale access policy")
	}
	if _, err := st.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: "/tmp/v1", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	app, err = st.GetAppBySlug("one")
	if err != nil {
		t.Fatal(err)
	}
	if app.CurrentVersion != "v1" {
		t.Fatalf("stale deployment summary: %+v", app)
	}
	if _, err := st.GetAppBySlug("missing"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("missing app: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAppBySlug("one"); err == nil {
		t.Fatal("lookup after close should fail")
	}
}

func BenchmarkGetAppBySlug(b *testing.B) {
	st := dbtest.New(b)
	if err := st.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		b.Fatal(err)
	}
	owner, err := st.GetUserByUsername("owner")
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := st.CreateApp(db.CreateAppParams{Slug: fmt.Sprintf("app-%d", i), Name: "App", OwnerID: owner.ID}); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := st.GetAppBySlug("app-5"); err != nil {
			b.Fatal(err)
		}
	}
}
