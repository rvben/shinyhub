package process

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func anySliceHas(v any, want string) bool {
	s, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range s {
		if str, ok := e.(string); ok && str == want {
			return true
		}
	}
	return false
}

func TestDockerRuntimeStart_HardensContainer(t *testing.T) {
	var captured map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": "c1"})
	})
	mux.HandleFunc("/containers/c1/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rt := newDockerRuntimeWithServer(t, mux)
	_, err := rt.Start(context.Background(), StartParams{
		Slug:    "app",
		Dir:     t.TempDir(),
		Command: []string{"uv", "run", "x"},
		Port:    20055,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	host, ok := captured["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("no HostConfig in create body: %v", captured)
	}
	if !anySliceHas(host["SecurityOpt"], "no-new-privileges:true") {
		t.Errorf("HostConfig.SecurityOpt missing no-new-privileges:true: %v", host["SecurityOpt"])
	}
	if !anySliceHas(host["CapDrop"], "ALL") {
		t.Errorf("HostConfig.CapDrop missing ALL: %v", host["CapDrop"])
	}
	if pl, ok := host["PidsLimit"].(float64); !ok || pl <= 0 {
		t.Errorf("HostConfig.PidsLimit not set to a positive value: %v", host["PidsLimit"])
	}

	// The app bundle (/app) must be mounted read-only so a compromised app
	// cannot mutate its own deployed code; /app-data remains writable.
	mounts, _ := host["Mounts"].([]any)
	var foundApp, appReadOnly bool
	for _, m := range mounts {
		mm, _ := m.(map[string]any)
		if mm["Target"] == "/app" {
			foundApp = true
			appReadOnly, _ = mm["ReadOnly"].(bool)
		}
	}
	if !foundApp {
		t.Fatalf("no /app mount in HostConfig: %v", host["Mounts"])
	}
	if !appReadOnly {
		t.Error("/app bundle mount must be ReadOnly")
	}
}
