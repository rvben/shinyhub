package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAnnouncementsPublish(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":"release1","revision":1,"status":"active","ends_at":"2030-01-01T00:00:00Z"}`)
	before := time.Now()
	out, err := execCLI(t, "announcements", "publish", "--title", " Release ", "--message", " Save work ", "--severity", "critical", "--ttl", "60m", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests: %+v", *reqs)
	}
	r := (*reqs)[0]
	if r.Method != "POST" || r.Path != "/api/announcements" || r.Auth != "Token shk_test" {
		t.Fatalf("request: %+v", r)
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "Release" || body["message"] != "Save work" || body["dismissible"] != false || body["publication"] != "published" {
		t.Fatalf("body: %v", body)
	}
	end, err := time.Parse(time.RFC3339Nano, body["ends_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if end.Before(before.Add(time.Hour)) || end.After(time.Now().Add(time.Hour)) {
		t.Fatalf("expiry: %s", end)
	}
	if _, ok := body["starts_at"]; ok {
		t.Fatal("publication time should come from server")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env["status"] != "published" || env["id"] != "release1" || env["announcement"].(map[string]any)["status"] != "active" {
		t.Fatalf("output: %s", out)
	}
}

func TestAnnouncementsPublishValidation(t *testing.T) {
	cases := [][]string{
		{}, {"--ttl", "0s"}, {"--ttl", "-1m"}, {"--ttl", "1m", "--ends-at", "2030-01-01T00:00:00Z"},
		{"--ends-at", "2020-01-01T00:00:00Z"}, {"--ends-at", "2030-01-01"},
		{"--ttl", "1m", "--severity", "bad"}, {"--ttl", "1m", "--details-url", "javascript:alert(1)"},
		{"--ttl", "1m", "--title", " "}, {"--ttl", "1m", "--title", ""}, {"--ttl", "1m", "--title", strings.Repeat("x", 121)}, {"--ttl", "1m", "--message", strings.Repeat("x", 601)},
		{"--ttl", "1m", "-o", "id"}, {"--ttl", "1m", "-o", "ndjson"},
	}
	for _, flags := range cases {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			_, reqs, _ := setupCLITest(t)
			args := append([]string{"announcements", "publish", "--title", "Release", "--message", "Save work"}, flags...)
			_, err := execCLI(t, args...)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if len(*reqs) != 0 {
				t.Fatalf("invalid input made requests: %+v", *reqs)
			}
		})
	}
}

func TestAnnouncementsPublishAbsoluteExpiryAndDismissal(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":"release1"}`)
	_, err := execCLI(t, "announcements", "publish", "--title", "Release", "--message", "Save work", "--severity", "critical", "--dismissible=true", "--ends-at", "2030-01-01T01:00:00+01:00")
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests: %+v", *reqs)
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["ends_at"] != "2030-01-01T00:00:00Z" || body["dismissible"] != true {
		t.Fatalf("body: %v", body)
	}
}

func TestAnnouncementsDisable(t *testing.T) {
	for _, state := range []string{"published", "draft", "disabled", "archived"} {
		t.Run(state, func(t *testing.T) {
			_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprintf(w, `{"id":"release1","publication":%q,"revision":7}`, state)
					return
				}
				fmt.Fprint(w, `{"id":"release1","publication":"disabled","revision":8}`)
			})
			out, err := execCLI(t, "announcements", "disable", "release1", "-o", "json")
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if state == "published" || state == "draft" {
				want = 2
			}
			if len(*reqs) != want {
				t.Fatalf("requests: %+v", *reqs)
			}
			if want == 2 {
				r := (*reqs)[1]
				if r.Method != "PATCH" || r.Path != "/api/announcements/release1" {
					t.Fatalf("request: %+v", r)
				}
				var body map[string]any
				if err := json.Unmarshal(r.Body, &body); err != nil {
					t.Fatal(err)
				}
				if body["expected_revision"] != float64(7) || body["publication"] != "disabled" {
					t.Fatalf("body: %v", body)
				}
			}
			if !strings.Contains(out, `"status":"disabled"`) {
				t.Fatal(out)
			}
		})
	}
}

func TestAnnouncementsDisableConflictNotRetried(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `{"id":"release1","publication":"published","revision":3}`)
			return
		}
		w.WriteHeader(409)
		fmt.Fprint(w, `{"error":"announcement changed; reload before saving"}`)
	})
	_, err := execCLI(t, "announcements", "disable", "release1")
	if err == nil || len(*reqs) != 2 {
		t.Fatalf("err=%v requests=%+v", err, *reqs)
	}
	if kind, code := classify(err); kind != KindConflict || code != 5 {
		t.Fatalf("classification: %s/%d", kind, code)
	}
}

func TestAnnouncementsReadAndErrors(t *testing.T) {
	for _, status := range []int{200, 403, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			_, reqs, setResp := setupCLITest(t)
			setResp(status, `{"id":"release1","title":"Release","error":"unavailable"}`)
			out, err := execCLI(t, "announcements", "get", "release1", "-o", "json")
			if (err == nil) != (status == 200) {
				t.Fatalf("status %d err=%v", status, err)
			}
			if status != 200 {
				wantKind, wantCode := statusKind(status)
				if kind, code := classify(err); kind != wantKind || code != wantCode {
					t.Fatalf("classification: %s/%d", kind, code)
				}
			}
			if status == 200 && !strings.Contains(out, `"id":"release1"`) {
				t.Fatal(out)
			}
			if len(*reqs) != 1 || (*reqs)[0].Path != "/api/announcements/release1" {
				t.Fatalf("requests: %+v", *reqs)
			}
		})
	}
	for _, id := range []string{"..", ".", "a/b", " ", "a\\b"} {
		t.Run(id, func(t *testing.T) {
			_, reqs, _ := setupCLITest(t)
			_, err := execCLI(t, "announcements", "disable", id)
			if err == nil || len(*reqs) != 0 {
				t.Fatalf("err=%v requests=%+v", err, *reqs)
			}
		})
	}
}

func TestAnnouncementsListPagination(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("offset") {
		case "0":
			fmt.Fprint(w, `{"announcements":[{"id":"one"},{"id":"two"}],"has_more":true}`)
		case "2":
			fmt.Fprint(w, `{"announcements":[{"id":"three"}],"has_more":false}`)
		default:
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
	})
	out, err := execCLI(t, "announcements", "list", "--limit", "1", "--offset", "1", "--fields", "id", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items                []map[string]any
		Total, Limit, Offset int
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Total != 3 || env.Limit != 1 || env.Offset != 1 || len(env.Items) != 1 || env.Items[0]["id"] != "two" {
		t.Fatal(out)
	}
	if len(*reqs) != 2 {
		t.Fatalf("requests: %+v", *reqs)
	}
}

func TestAnnouncementsListRejectsBrokenPagination(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{"announcements":[],"has_more":true}`)
	_, err := execCLI(t, "announcements", "list")
	if err == nil {
		t.Fatal("expected protocol error")
	}
	count := 0
	for _, r := range *reqs {
		if r.Path == "/api/announcements" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("list was retried: %+v", *reqs)
	}
}

func TestAnnouncementsPublishFailureNeverRetries(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{403, `{"error":"forbidden"}`}, {503, `{"error":"unavailable"}`}, {201, `{}`}, {201, `null`}, {201, `<html>bad gateway</html>`}} {
		t.Run(fmt.Sprintf("%d-%s", tc.status, tc.body), func(t *testing.T) {
			_, reqs, setResp := setupCLITest(t)
			setResp(tc.status, tc.body)
			_, err := execCLI(t, "announcements", "publish", "--title", "Release", "--message", "Save work", "--ttl", "60m")
			if err == nil {
				t.Fatal("expected error")
			}
			writes := 0
			for _, r := range *reqs {
				if r.Method == "POST" {
					writes++
				}
			}
			if writes != 1 {
				t.Fatalf("publication retried: %+v", *reqs)
			}
		})
	}
}

func TestAnnouncementsListEmpty(t *testing.T) {
	_, _, setResp := setupCLITest(t)
	setResp(200, `{"announcements":[],"has_more":false}`)
	out, err := execCLI(t, "announcements", "list", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Items []map[string]any
		Total int
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Items == nil || len(env.Items) != 0 || env.Total != 0 {
		t.Fatal(out)
	}
}

func TestAnnouncementsDisableRejectsPartialWriteResponse(t *testing.T) {
	_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			fmt.Fprint(w, `{"id":"release1","publication":"published","revision":3}`)
			return
		}
		fmt.Fprint(w, `{"publication":"disabled"}`)
	})
	_, err := execCLI(t, "announcements", "disable", "release1")
	if err == nil {
		t.Fatal("expected protocol error")
	}
	patches := 0
	for _, r := range *reqs {
		if r.Method == "PATCH" {
			patches++
		}
	}
	if patches != 1 {
		t.Fatalf("withdrawal retried: %+v", *reqs)
	}
}

func assertAnnouncementUnknown(t *testing.T, err error, action string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected ambiguous-outcome error")
	}
	if kind, code := classify(err); kind != KindInternal || code != 1 {
		t.Fatalf("classification %s/%d: %v", kind, code, err)
	}
	var hinted interface{ Hint() string }
	if !errors.As(err, &hinted) || !strings.Contains(hinted.Hint(), action+" may have succeeded") {
		t.Fatalf("missing inspection hint: %v", err)
	}
	var stderr bytes.Buffer
	reportTo(&stderr, false, formatJSON, err)
	if !strings.Contains(stderr.String(), action+" may have succeeded") {
		t.Fatalf("hint missing from stderr: %s", stderr.String())
	}
}

func TestAnnouncementsPublishAmbiguousOutcome(t *testing.T) {
	for _, mode := range []string{"disconnect", "timeout", "502", "504", "html", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			oldClient := httpClient
			httpClient = &apiClient{&http.Client{Timeout: 100 * time.Millisecond}}
			t.Cleanup(func() { httpClient = oldClient })
			_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
				case "timeout":
					<-r.Context().Done()
				case "502":
					w.WriteHeader(502)
					fmt.Fprint(w, "Bad Gateway")
				case "504":
					w.WriteHeader(504)
					fmt.Fprint(w, "Gateway Timeout")
				case "html":
					w.WriteHeader(201)
					fmt.Fprint(w, "<html>proxy error</html>")
				case "truncated":
					w.WriteHeader(201)
					fmt.Fprint(w, `{"id":`)
				}
			})
			_, err := execCLI(t, "announcements", "publish", "--title", "Release", "--message", "Save work", "--ttl", "60m", "-o", "json")
			assertAnnouncementUnknown(t, err, "publication")
			if len(*reqs) != 1 || (*reqs)[0].Method != "POST" {
				t.Fatalf("requests: %+v", *reqs)
			}
			if mode == "timeout" || mode == "disconnect" {
				var ue *url.Error
				if !errors.As(err, &ue) {
					t.Fatalf("lost transport cause: %v", err)
				}
			}
		})
	}
}

func TestAnnouncementsExpectedRevision(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		expected    int64
		wantCalls   int
		wantKind    Kind
	}{
		{"matching", "published", 7, 2, ""}, {"changed", "published", 1, 1, KindConflict},
		{"disabled", "disabled", 1, 1, ""}, {"archived", "archived", 1, 1, ""},
		{"zero", "published", 0, 0, KindValidation}, {"negative", "published", -1, 0, KindValidation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprintf(w, `{"id":"release1","publication":%q,"revision":7}`, tc.state)
					return
				}
				fmt.Fprint(w, `{"id":"release1","publication":"disabled","revision":8}`)
			})
			_, err := execCLI(t, "announcements", "disable", "release1", "--expected-revision", fmt.Sprint(tc.expected))
			if tc.wantKind == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantKind != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if kind, _ := classify(err); kind != tc.wantKind {
					t.Fatalf("kind: %s", kind)
				}
			}
			if len(*reqs) != tc.wantCalls {
				t.Fatalf("requests: %+v", *reqs)
			}
			if tc.wantCalls == 2 {
				var body map[string]any
				if err := json.Unmarshal((*reqs)[1].Body, &body); err != nil {
					t.Fatal(err)
				}
				if body["expected_revision"] != float64(7) {
					t.Fatalf("body: %v", body)
				}
			}
		})
	}
}

func TestAnnouncementsDisableInvalidReadNeverWrites(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		kind   Kind
	}{
		{200, `{"id":"other","publication":"published","revision":1}`, KindInternal},
		{200, `{"id":"release1","publication":"published","revision":0}`, KindInternal},
		{200, `{"id":"release1","publication":"weird","revision":1}`, KindInternal},
		{200, `<html>wrong server</html>`, KindInternal},
		{404, `{"error":"not found"}`, KindNotFound},
	} {
		t.Run(tc.body, func(t *testing.T) {
			_, reqs, setResp := setupCLITest(t)
			setResp(tc.status, tc.body)
			_, err := execCLI(t, "announcements", "disable", "release1")
			if err == nil {
				t.Fatal("expected error")
			}
			if kind, _ := classify(err); kind != tc.kind {
				t.Fatalf("kind: %s", kind)
			}
			for _, r := range *reqs {
				if r.Method != "GET" {
					t.Fatalf("unexpected write: %+v", r)
				}
			}
		})
	}
}

func TestAnnouncementsDisableWriteErrors(t *testing.T) {
	for _, status := range []int{403, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			_, reqs := setupCLITestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, `{"id":"release1","publication":"published","revision":1}`)
					return
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"rejected"}`)
			})
			_, err := execCLI(t, "announcements", "disable", "release1")
			if status == 403 {
				if kind, code := classify(err); kind != KindAuth || code != 3 {
					t.Fatalf("classification %s/%d", kind, code)
				}
			} else {
				assertAnnouncementUnknown(t, err, "withdrawal")
			}
			if len(*reqs) != 2 || (*reqs)[1].Method != "PATCH" {
				t.Fatalf("requests: %+v", *reqs)
			}
		})
	}
}

func TestAnnouncementsPublishTitleBoundaryAndFractionalExpiry(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(201, `{"id":"release1","revision":1}`)
	_, err := execCLI(t, "announcements", "publish", "--title", strings.Repeat("界", 120), "--message", "Save work", "--ends-at", "2030-01-01T01:00:00.123456789+01:00")
	if err != nil {
		t.Fatal(err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("requests: %+v", *reqs)
	}
	var body map[string]any
	if err := json.Unmarshal((*reqs)[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["ends_at"] != "2030-01-01T00:00:00.123456789Z" {
		t.Fatalf("expiry: %v", body["ends_at"])
	}
}
