package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// lastEnvValue returns the value the child sees for key: the last entry wins.
func lastEnvValue(env []string, key string) (string, bool) {
	var val string
	var found bool
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			val, found = v, true
		}
	}
	return val, found
}

func TestNativeChildEnv_PointsAtTheCacheNamespace(t *testing.T) {
	env := nativeChildEnv(StartParams{
		Env:           []string{"SHINYHUB_CACHE_DIR=/evil", "SHINYHUB_CACHE_MAX_MB=1"},
		AppCachePath:  "/srv/cache/demo/d7",
		AppCacheMaxMB: 256,
	})
	if v, _ := lastEnvValue(env, "SHINYHUB_CACHE_DIR"); v != "/srv/cache/demo/d7" {
		t.Errorf("SHINYHUB_CACHE_DIR = %q, want the platform namespace over the app's own value", v)
	}
	if v, _ := lastEnvValue(env, "SHINYHUB_CACHE_MAX_MB"); v != "256" {
		t.Errorf("SHINYHUB_CACHE_MAX_MB = %q, want 256", v)
	}
}

func TestNativeChildEnv_NoCacheWithoutANamespace(t *testing.T) {
	env := nativeChildEnv(StartParams{AppCacheMaxMB: 256})
	for _, key := range []string{"SHINYHUB_CACHE_DIR", "SHINYHUB_CACHE_MAX_MB"} {
		if _, ok := lastEnvValue(env, key); ok {
			t.Errorf("%s set although no namespace was provisioned", key)
		}
	}
}

// dockerCreateCapture serves the Docker API calls Start and RunOnce make and
// records the container create body.
func dockerCreateCapture(t *testing.T, id string) (*http.ServeMux, *map[string]any) {
	t.Helper()
	var captured map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/containers/create", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"Id": id})
	})
	mux.HandleFunc("/containers/"+id+"/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/containers/"+id+"/attach", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/containers/"+id+"/wait", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int{"StatusCode": 0})
	})
	return mux, &captured
}

func assertCacheMount(t *testing.T, body map[string]any, hostPath string, want bool) {
	t.Helper()
	host, _ := body["HostConfig"].(map[string]any)
	mounts, _ := host["Mounts"].([]any)
	var got []map[string]any
	for _, m := range mounts {
		mm, _ := m.(map[string]any)
		if mm["Target"] == containerCacheDir {
			got = append(got, mm)
		}
	}
	envRaw, _ := body["Env"].([]any)
	var env []string
	for _, e := range envRaw {
		s, _ := e.(string)
		env = append(env, s)
	}
	dir, dirSet := lastEnvValue(env, "SHINYHUB_CACHE_DIR")
	if !want {
		if len(got) != 0 || dirSet {
			t.Fatalf("cache wired without a namespace: mounts %v, SHINYHUB_CACHE_DIR set=%v", got, dirSet)
		}
		return
	}
	if len(got) != 1 || got[0]["Source"] != hostPath || got[0]["ReadOnly"] == true {
		t.Fatalf("cache mounts = %v, want one writable mount of %s at %s", got, hostPath, containerCacheDir)
	}
	if dir != containerCacheDir {
		t.Errorf("SHINYHUB_CACHE_DIR = %q, want the in-container path %s", dir, containerCacheDir)
	}
	if v, _ := lastEnvValue(env, "SHINYHUB_CACHE_MAX_MB"); v != "128" {
		t.Errorf("SHINYHUB_CACHE_MAX_MB = %q, want 128", v)
	}
	if slices.Contains(env, "SHINYHUB_CACHE_DIR="+hostPath) {
		t.Error("the host path leaked into the container env")
	}
}

func TestDockerRuntime_MountsTheCacheNamespace(t *testing.T) {
	const host = "/srv/cache/demo/d7"
	for name, run := range map[string]func(rt *DockerRuntime, p StartParams) error{
		"start": func(rt *DockerRuntime, p StartParams) error {
			_, err := rt.Start(context.Background(), p, &bytes.Buffer{})
			return err
		},
		"run once": func(rt *DockerRuntime, p StartParams) error {
			_, err := rt.RunOnce(context.Background(), p, &bytes.Buffer{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, withCache := range []bool{true, false} {
				mux, captured := dockerCreateCapture(t, "cont-cache")
				rt := newDockerRuntimeWithServer(t, mux)
				p := StartParams{
					Slug: "demo", Dir: t.TempDir(), Port: 9999,
					Command: []string{"Rscript", "-e", "1"},
				}
				if withCache {
					p.AppCachePath, p.AppCacheMaxMB = host, 128
					p.Env = []string{"SHINYHUB_CACHE_DIR=/evil"}
				}
				if err := run(rt, p); err != nil {
					t.Fatalf("launch: %v", err)
				}
				assertCacheMount(t, *captured, host, withCache)
			}
		})
	}
}

// A scheduled container writes the same bundle, data and cache mounts as a
// serving one, under the same dropped capabilities, so it must run as the
// same bundle owner or a non-root service's files are unwritable to it.
func TestDockerRuntimeRunOnce_RunsAsBundleOwner(t *testing.T) {
	mux, captured := dockerCreateCapture(t, "cont-once")
	rt := newDockerRuntimeWithServer(t, mux)
	dir := t.TempDir()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	if _, err := rt.RunOnce(context.Background(), StartParams{
		Slug: "demo", Dir: dir, Command: []string{"Rscript", "job.R"},
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	want := fmt.Sprintf("%d:%d", st.Uid, st.Gid)
	if got, _ := (*captured)["User"].(string); got != want {
		t.Errorf("one-shot container User = %q, want the bundle owner %q", got, want)
	}
}
