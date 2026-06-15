package process

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDockerClient_PauseUnpause(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := newTestDockerClient(srv)

	if err := c.pauseContainer("abc"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := c.unpauseContainer("abc"); err != nil {
		t.Fatalf("unpause: %v", err)
	}

	want := []string{"POST /containers/abc/pause", "POST /containers/abc/unpause"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestDockerClient_PauseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := newTestDockerClient(srv)

	if err := c.pauseContainer("abc"); err == nil {
		t.Fatal("expected error on 500 from pause")
	}
}
