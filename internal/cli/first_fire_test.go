package cli

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestFirstFireRefsFromDeployResponse(t *testing.T) {
	body := []byte(`{"deploy_count":3,"manifest":{"schedules":[
		{"name":"warm","action":"created","schedule_id":5,"first_fire":{"run_id":42}},
		{"name":"other","action":"updated","schedule_id":6}
	]}}`)
	refs := firstFireRefsFromDeployResponse(body)
	if len(refs) != 1 {
		t.Fatalf("len(refs) = %d, want 1", len(refs))
	}
	if refs[0].Schedule != "warm" || refs[0].ScheduleID != 5 || refs[0].RunID != 42 {
		t.Errorf("ref = %+v, want {warm 5 42}", refs[0])
	}
}

func TestFirstFireRefsFromDeployResponse_None(t *testing.T) {
	if got := firstFireRefsFromDeployResponse([]byte(`{"deploy_count":1}`)); len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestWaitForFirstFireLoop_Succeeded(t *testing.T) {
	statuses := []string{"running", "running", "succeeded"}
	i := 0
	poll := func() (string, error) {
		s := statuses[i]
		if i < len(statuses)-1 {
			i++
		}
		return s, nil
	}
	now := func() time.Time { return time.Unix(0, 0) }
	status, err := waitForFirstFireLoop(poll, 10*time.Second, time.Millisecond, time.Hour, now, func(time.Duration) {}, io.Discard, "warm")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if status != "succeeded" {
		t.Errorf("status = %q, want succeeded", status)
	}
}

func TestWaitForFirstFireLoop_SkippedOverlapIsNotFailure(t *testing.T) {
	poll := func() (string, error) { return "skipped_overlap", nil }
	now := func() time.Time { return time.Unix(0, 0) }
	status, err := waitForFirstFireLoop(poll, 10*time.Second, time.Millisecond, time.Hour, now, func(time.Duration) {}, io.Discard, "warm")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !firstFireStatusOK(status) {
		t.Errorf("firstFireStatusOK(%q) = false, want true", status)
	}
}

func TestWaitForFirstFireLoop_TransientErrorThenSucceeds(t *testing.T) {
	i := 0
	responses := []struct {
		status string
		err    error
	}{
		{"", &httpStatusError{statusCode: 503}},
		{"", &httpStatusError{statusCode: 503}},
		{"succeeded", nil},
	}
	poll := func() (string, error) {
		r := responses[i]
		if i < len(responses)-1 {
			i++
		}
		return r.status, r.err
	}
	cur := time.Unix(0, 0)
	now := func() time.Time { return cur }
	sleep := func(d time.Duration) { cur = cur.Add(d) }
	status, err := waitForFirstFireLoop(poll, 10*time.Second, time.Millisecond, time.Hour, now, sleep, io.Discard, "warm")
	if err != nil {
		t.Fatalf("err = %v, want nil (transient errors should be skipped)", err)
	}
	if status != "succeeded" {
		t.Errorf("status = %q, want succeeded", status)
	}
}

func TestWaitForFirstFireLoop_FatalErrorAborts(t *testing.T) {
	fatalErr := &httpStatusError{statusCode: 401}
	poll := func() (string, error) { return "", fatalErr }
	cur := time.Unix(0, 0)
	now := func() time.Time { return cur }
	sleep := func(d time.Duration) { cur = cur.Add(d) }
	_, err := waitForFirstFireLoop(poll, 10*time.Second, time.Second, time.Hour, now, sleep, io.Discard, "warm")
	if err == nil {
		t.Fatal("err = nil, want fatal error")
	}
	if errors.Is(err, errFirstFireTimeout) {
		t.Fatal("err = errFirstFireTimeout, want the 401 error (should abort immediately)")
	}
	var he *httpStatusError
	if !errors.As(err, &he) || he.statusCode != 401 {
		t.Errorf("err = %v, want *httpStatusError with statusCode 401", err)
	}
	// Clock must not have advanced to the deadline (abort was immediate).
	if cur != time.Unix(0, 0) {
		t.Errorf("clock advanced to %v, want no advancement (immediate abort)", cur)
	}
}

func TestWaitForFirstFireLoop_Timeout(t *testing.T) {
	poll := func() (string, error) { return "running", nil }
	cur := time.Unix(0, 0)
	now := func() time.Time { return cur }
	sleep := func(d time.Duration) { cur = cur.Add(d) }
	var out bytes.Buffer
	status, err := waitForFirstFireLoop(poll, 5*time.Second, time.Second, time.Hour, now, sleep, &out, "warm")
	if !errors.Is(err, errFirstFireTimeout) {
		t.Fatalf("err = %v, want errFirstFireTimeout", err)
	}
	if status != "running" {
		t.Errorf("status = %q, want running (last seen)", status)
	}
}

func TestFirstFireStatusOK(t *testing.T) {
	for _, s := range []string{"succeeded", "skipped_overlap"} {
		if !firstFireStatusOK(s) {
			t.Errorf("firstFireStatusOK(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"failed", "interrupted", "cancelled", "timed_out"} {
		if firstFireStatusOK(s) {
			t.Errorf("firstFireStatusOK(%q) = true, want false", s)
		}
	}
}
