package lifecycle

import (
	"errors"
	"testing"

	"github.com/rvben/shinyhub/internal/deploy"
)

func TestWakeReplica_ResumesSuspended(t *testing.T) {
	w := New(Config{}, nil, nil, nil,
		func(string, string, int) (*deploy.Result, error) {
			t.Fatalf("cold deploy should not run for a resumable replica")
			return nil, nil
		})
	var resumed bool
	w.SetResume(func(string, string, int) (*deploy.Result, error) {
		resumed = true
		return &deploy.Result{Index: 0, EndpointURL: "http://127.0.0.1:2500"}, nil
	})

	res, booted, err := w.wakeReplica("app", "/bundle", 0, true)
	if err != nil || booted || !resumed || res == nil || res.EndpointURL == "" {
		t.Fatalf("wakeReplica = (%v,booted=%v,%v) resumed=%v", res, booted, err, resumed)
	}
}

func TestWakeReplica_FallsBackToColdBoot_OnResumeError(t *testing.T) {
	var coldBooted bool
	w := New(Config{}, nil, nil, nil,
		func(string, string, int) (*deploy.Result, error) {
			coldBooted = true
			return &deploy.Result{Index: 0, EndpointURL: "http://127.0.0.1:3000"}, nil
		})
	w.SetResume(func(string, string, int) (*deploy.Result, error) {
		return nil, errors.New("snapshot gone")
	})

	res, booted, err := w.wakeReplica("app", "/bundle", 0, true)
	if err != nil || !booted || !coldBooted || res == nil || res.EndpointURL != "http://127.0.0.1:3000" {
		t.Fatalf("wakeReplica = (%v,booted=%v,%v) coldBooted=%v", res, booted, err, coldBooted)
	}
}

func TestWakeReplica_ColdBoots_WhenNotSuspended(t *testing.T) {
	var coldBooted bool
	w := New(Config{}, nil, nil, nil,
		func(string, string, int) (*deploy.Result, error) {
			coldBooted = true
			return &deploy.Result{Index: 0}, nil
		})
	w.SetResume(func(string, string, int) (*deploy.Result, error) {
		t.Fatalf("resume should not run for a non-suspended replica")
		return nil, nil
	})

	if _, booted, err := w.wakeReplica("app", "/bundle", 0, false); err != nil || !booted || !coldBooted {
		t.Fatalf("booted=%v err=%v coldBooted=%v", booted, err, coldBooted)
	}
}
