package db_test

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// mkApp creates an app whose project_slug is EXACTLY what the caller asked for,
// including "".
//
// CreateApp still omits the project_slug column entirely when ProjectSlug is ""
// (queries.go:801-805), which lets migration 001's legacy `DEFAULT 'default'`
// apply. On SQLite that default survives migration 050 (SQLite cannot alter a
// column default in place), so a fixture written as "ungrouped" would silently
// land in a project literally named "default" - and every assertion about "" in
// this file would pass while testing nothing. Task 5 deletes that branch; this
// helper is what makes "" mean "" until then, and it stays correct afterwards.
//
// PatchAppSettings is used rather than raw SQL because s.DB() returns the
// unrebound handle: a literal `?` placeholder breaks when the suite runs against
// SHINYHUB_TEST_POSTGRES_DSN.
func mkApp(t *testing.T, s *db.Store, slug, project, access string, ownerID int64) {
	t.Helper()
	if err := s.CreateApp(db.CreateAppParams{
		Slug: slug, Name: slug, ProjectSlug: project, OwnerID: ownerID, Access: access,
	}); err != nil {
		t.Fatalf("create app %s: %v", slug, err)
	}
	if project != "" {
		return
	}
	if _, _, _, _, err := s.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: slug, SetProjectSlug: true, ProjectSlug: "",
	}); err != nil {
		t.Fatalf("clear project on %s: %v", slug, err)
	}
	got, err := s.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	// Positive control on the helper itself: if this ever reads "default", the
	// fixture is lying and every "" assertion below is vacuous.
	if got.ProjectSlug != "" {
		t.Fatalf("mkApp(%s, \"\") produced project_slug %q", slug, got.ProjectSlug)
	}
}

func TestUpsertProjectIsIdempotentAndNeverOverwrites(t *testing.T) {
	s := mustOpenDB(t)

	created, err := s.UpsertProject(db.Project{Slug: "analytics", Name: "Analytics", IconEmoji: "\U0001F4CA"})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !created {
		t.Error("first upsert must report created=true")
	}

	// A second upsert with different metadata must NOT clobber the stored
	// values: POST /api/projects is idempotent and only UpdateProject renames.
	created, err = s.UpsertProject(db.Project{Slug: "analytics", Name: "Something Else", IconEmoji: "\U0001F41B"})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created {
		t.Error("second upsert must report created=false")
	}
	got, err := s.GetProject("analytics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Analytics" || got.IconEmoji != "\U0001F4CA" {
		t.Errorf("upsert overwrote metadata: name=%q icon=%q", got.Name, got.IconEmoji)
	}
}

func TestEnsureProjectCreatesBareRow(t *testing.T) {
	s := mustOpenDB(t)
	created, err := s.EnsureProject("platform")
	if err != nil || !created {
		t.Fatalf("EnsureProject = %v, %v; want true, nil", created, err)
	}
	p, err := s.GetProject("platform")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.Name != "" || p.Description != "" || p.IconEmoji != "" {
		t.Errorf("EnsureProject must create a bare row, got %+v", p)
	}
	created, err = s.EnsureProject("platform")
	if err != nil || created {
		t.Fatalf("second EnsureProject = %v, %v; want false, nil", created, err)
	}
}

// TestEnsureProjectEmptySlugIsNoop guards the early return in EnsureProject:
// without it, EnsureProject("") would INSERT a projects row with an empty
// slug, breaking the invariant that an empty slug means "no project" and has
// no row at all.
func TestEnsureProjectEmptySlugIsNoop(t *testing.T) {
	s := mustOpenDB(t)
	created, err := s.EnsureProject("")
	if err != nil || created {
		t.Fatalf("EnsureProject(\"\") = %v, %v; want false, nil", created, err)
	}
	if _, err := s.GetProject(""); !errors.Is(err, db.ErrProjectNotFound) {
		t.Errorf("GetProject(\"\") after EnsureProject(\"\") error = %v, want ErrProjectNotFound", err)
	}
}

func TestGetProjectNotFound(t *testing.T) {
	s := mustOpenDB(t)
	if _, err := s.GetProject("nope"); !errors.Is(err, db.ErrProjectNotFound) {
		t.Errorf("GetProject(missing) error = %v, want ErrProjectNotFound", err)
	}
}

func TestUpdateProjectSetsOnlyRequestedFields(t *testing.T) {
	s := mustOpenDB(t)
	if _, err := s.UpsertProject(db.Project{Slug: "p", Name: "Old", Description: "D", IconEmoji: "\U0001F680"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProject(db.UpdateProjectParams{Slug: "p", SetName: true, Name: "New"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetProject("p")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "New" {
		t.Errorf("name = %q, want New", got.Name)
	}
	if got.Description != "D" || got.IconEmoji != "\U0001F680" {
		t.Errorf("unset fields were clobbered: %+v", got)
	}
	// Clearing is an explicit Set with an empty value, not an omission.
	if err := s.UpdateProject(db.UpdateProjectParams{Slug: "p", SetIconEmoji: true, IconEmoji: ""}); err != nil {
		t.Fatalf("clear icon: %v", err)
	}
	got, _ = s.GetProject("p")
	if got.IconEmoji != "" {
		t.Errorf("icon = %q, want cleared", got.IconEmoji)
	}
	if err := s.UpdateProject(db.UpdateProjectParams{Slug: "missing", SetName: true, Name: "x"}); !errors.Is(err, db.ErrProjectNotFound) {
		t.Errorf("update missing = %v, want ErrProjectNotFound", err)
	}
}

func TestDeleteProjectAndCountApps(t *testing.T) {
	s := mustOpenDB(t)
	u := mustCreateUser(t, s, "owner", "admin")
	if _, err := s.UpsertProject(db.Project{Slug: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateApp(db.CreateAppParams{Slug: "a", Name: "A", ProjectSlug: "p", OwnerID: u.ID, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountAppsInProject("p")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("CountAppsInProject = %d, want 1", n)
	}
	// Deleting is allowed at the store layer; the 409 guard lives in the
	// handler, which calls CountAppsInProject first. The store must not
	// cascade: apps.project_slug is a soft reference.
	if err := s.DeleteProject("p"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	app, err := s.GetAppBySlug("a")
	if err != nil {
		t.Fatal(err)
	}
	if app.ProjectSlug != "p" {
		t.Errorf("delete must not cascade into apps, project_slug = %q", app.ProjectSlug)
	}
	if err := s.DeleteProject("p"); !errors.Is(err, db.ErrProjectNotFound) {
		t.Errorf("delete missing = %v, want ErrProjectNotFound", err)
	}
}

func TestListProjectsOrderedBySlugWithAppCounts(t *testing.T) {
	s := mustOpenDB(t)
	u := mustCreateUser(t, s, "owner", "admin")
	for _, sl := range []string{"zeta", "alpha", "mid"} {
		if _, err := s.UpsertProject(db.Project{Slug: sl}); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []struct{ slug, project string }{
		{"one", "alpha"}, {"two", "alpha"}, {"three", "mid"}, {"loose", ""},
	} {
		mkApp(t, s, a.slug, a.project, "private", u.ID)
	}

	ps, err := s.ListProjects(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	counts := map[string]int{}
	for _, p := range ps {
		got = append(got, p.Slug)
		counts[p.Slug] = p.AppCount
	}
	want := []string{"alpha", "mid", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("ListProjects = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListProjects = %v, want %v", got, want)
		}
	}
	if counts["alpha"] != 2 || counts["mid"] != 1 {
		t.Errorf("app counts = %v, want alpha=2 mid=1", counts)
	}
	// A project nobody has assigned an app to must report 0, not vanish: it is
	// still selectable in the UI, and the correlated COUNT(*) subquery is what
	// keeps it in the list (it runs once per projects row regardless of matches).
	if counts["zeta"] != 0 {
		t.Errorf("empty project app_count = %d, want 0", counts["zeta"])
	}
}
