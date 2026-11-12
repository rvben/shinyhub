package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoAPISendsCredentialAndJSONBody(t *testing.T) {
	var gotAuth, gotType, gotMethod, gotURI, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		gotMethod, gotURI = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("X-ShinyHub-Warning", "heads up")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	res, err := doAPI(&cliConfig{Host: srv.URL, Token: "shk_test"}, apiCall{
		Method: "PATCH", Path: "/api/apps/demo?x=1", Body: []byte(`{"a":1}`), Op: "update app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Token shk_test" {
		t.Errorf("Authorization = %q, want the saved credential", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json for a request with a body", gotType)
	}
	if gotMethod != "PATCH" || gotURI != "/api/apps/demo?x=1" || gotBody != `{"a":1}` {
		t.Errorf("request = %s %s %q", gotMethod, gotURI, gotBody)
	}
	if res.Status != http.StatusCreated || string(res.Body) != `{"ok":true}` {
		t.Errorf("result = %d %q", res.Status, res.Body)
	}
	if got := res.Header.Get("X-ShinyHub-Warning"); got != "heads up" {
		t.Errorf("warning header = %q, want it passed through to the caller", got)
	}
}

func TestDoAPIOmitsContentTypeWithoutBody(t *testing.T) {
	var gotType string
	var gotLen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		gotLen = r.ContentLength
	}))
	defer srv.Close()

	if _, err := doAPI(&cliConfig{Host: srv.URL, Token: "shk_test"}, apiCall{Method: "POST", Path: "/api/apps/demo/stop", Op: "stop app"}); err != nil {
		t.Fatal(err)
	}
	if gotType != "" || gotLen != 0 {
		t.Errorf("bodyless request sent Content-Type %q and %d body bytes", gotType, gotLen)
	}
}

func TestDoAPIMapsFailureStatusToOperationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"not allowed"}`))
	}))
	defer srv.Close()

	res, err := doAPI(&cliConfig{Host: srv.URL, Token: "shk_test"}, apiCall{Method: "POST", Path: "/api/apps/demo/stop", Op: "stop app"})
	if err == nil {
		t.Fatalf("403 returned no error (result %+v)", res)
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != http.StatusForbidden {
		t.Fatalf("error = %#v, want an httpStatusError carrying 403", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "stop app") || !strings.Contains(msg, "not allowed") {
		t.Errorf("error %q should name the operation and the server's reason", msg)
	}
}

func TestDoAPIReturnsAcceptedFailureStatusAsResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"app not found"}`))
	}))
	defer srv.Close()
	cfg := &cliConfig{Host: srv.URL, Token: "shk_test"}

	res, err := doAPI(cfg, apiCall{Method: "DELETE", Path: "/api/apps/demo", Op: "delete app", Accept: []int{http.StatusNotFound}})
	if err != nil {
		t.Fatalf("accepted 404 returned an error: %v", err)
	}
	if res.Status != http.StatusNotFound || !strings.Contains(string(res.Body), "app not found") {
		t.Errorf("result = %d %q", res.Status, res.Body)
	}

	// A status the caller did not name is still an error.
	if _, err := doAPI(cfg, apiCall{Method: "DELETE", Path: "/api/apps/demo", Op: "delete app", Accept: []int{http.StatusConflict}}); err == nil {
		t.Error("404 with only 409 accepted returned no error")
	}
}
