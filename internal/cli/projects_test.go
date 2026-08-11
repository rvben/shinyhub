package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// lastBody decodes the most recent captured request body. capturedReq.Body is
// raw bytes, so tests unmarshal it themselves; the package has no decodeBody.
func lastBody(t *testing.T, reqs *[]capturedReq) map[string]any {
	t.Helper()
	if len(*reqs) == 0 {
		t.Fatal("no request was made")
	}
	var m map[string]any
	if err := json.Unmarshal((*reqs)[len(*reqs)-1].Body, &m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func TestAppsSetProjectSendsProjectSlug(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"a"}}`))
	})
	out, err := execCLI(t, "apps", "set", "a", "--project", "analytics", "-o", "table")
	if err != nil {
		t.Fatalf("apps set --project: %v", err)
	}
	if body := lastBody(t, reqs); body["project_slug"] != "analytics" {
		t.Errorf("body project_slug = %v, want analytics", body["project_slug"])
	}
	if !strings.Contains(out, "analytics") {
		t.Errorf("prose must name the project, got %q", out)
	}
}

func TestAppsSetProjectEmptyClears(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"app":{"slug":"a"}}`))
	})
	// "" is a real value (clear the project), so the flag must be keyed off
	// Changed, not off the value being non-empty.
	if _, err := execCLI(t, "apps", "set", "a", "--project", ""); err != nil {
		t.Fatalf("apps set --project '': %v", err)
	}
	v, present := lastBody(t, reqs)["project_slug"]
	if !present || v != "" {
		t.Errorf(`project_slug = %v (present=%v), want "" present`, v, present)
	}
}

func TestAppsSetProjectRejectsInvalidSlugLocally(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if _, err := execCLI(t, "apps", "set", "a", "--project", "Not A Slug"); err == nil {
		t.Error("apps set --project 'Not A Slug' must fail")
	}
	// Local validation means local: a round trip here would mean the CLI is
	// relying on the server to reject, and `apps set` would 400 instead of
	// naming the rule.
	if len(*reqs) != 0 {
		t.Errorf("made %d request(s); an invalid slug must fail without a round trip", len(*reqs))
	}
}

func TestProjectsListRendersItems(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"slug":"analytics","name":"Analytics","description":"","icon_emoji":"X","app_count":3}],"total":1}`))
	})
	out, err := execCLI(t, "projects", "list", "-o", "table")
	if err != nil {
		t.Fatalf("projects list: %v", err)
	}
	if !strings.Contains(out, "analytics") || !strings.Contains(out, "Analytics") {
		t.Errorf("projects list output missing slug or name: %q", out)
	}
	// The count is the whole reason to run this command over reading the
	// dashboard: it says which projects are actually carrying apps.
	if !strings.Contains(out, "3") {
		t.Errorf("projects list output missing app_count: %q", out)
	}
}

func TestProjectsSetCreatesThenUpdates(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPatch:
			w.WriteHeader(http.StatusNotFound) // does not exist yet
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"project":{"slug":"new","name":"New"}}`))
		}
	})
	if _, err := execCLI(t, "projects", "set", "new", "--name", "New"); err != nil {
		t.Fatalf("projects set: %v", err)
	}
	// set is upsert-shaped: PATCH first, fall back to POST on 404, so the same
	// command works whether or not the project exists.
	if len(*reqs) != 2 || (*reqs)[0].Method != http.MethodPatch || (*reqs)[1].Method != http.MethodPost {
		t.Errorf("requests = %+v, want PATCH then POST", *reqs)
	}
}

func TestProjectsRmSurfacesConflict(t *testing.T) {
	setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"project still has apps; move or delete them first"}`))
	})
	out, err := execCLI(t, "projects", "rm", "busy")
	if err == nil {
		t.Fatal("projects rm on a referenced project must fail")
	}
	if !strings.Contains(err.Error()+out, "still has apps") {
		t.Errorf("error must carry the server message, got %v / %q", err, out)
	}
	// httpError returns *httpStatusError, which classify maps 409 -> KindConflict
	// (errreport.go:26). A 409 reported as internal would exit 1 and read to a CI
	// gate as a client bug rather than "this project still has apps".
	if kind, _ := classify(err); kind != KindConflict {
		t.Errorf("kind = %q, want %q", kind, KindConflict)
	}
}
