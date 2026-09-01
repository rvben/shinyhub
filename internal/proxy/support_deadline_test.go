package proxy

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

// streamingBackend keeps a plain HTTP response open, flushing a line every
// few milliseconds until the proxy abandons the request. It models long
// polling and server-sent events: responses that never upgrade and so never
// receive the hijacked-connection deadline timer.
func streamingBackend(t *testing.T, cancelled *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				cancelled.Store(true)
				return
			case <-ticker.C:
				if _, err := w.Write([]byte("tick\n")); err != nil {
					cancelled.Store(true)
					return
				}
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// countLines reads the stream until it ends or ctx expires and reports the
// lines seen and how long the read lasted.
func countLines(ctx context.Context, t *testing.T, url string, user *auth.ContextUser) (lines int, elapsed time.Duration, err error) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if user != nil {
		req.Header.Set("X-Test-Support", "1")
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		lines++
	}
	return lines, time.Since(start), scanner.Err()
}

func TestSupportStreamingResponseEndsAtSessionDeadline(t *testing.T) {
	var cancelled atomic.Bool
	backend := streamingBackend(t, &cancelled)
	p := New()
	p.SetPoolSize("sales", 1)
	p.SetPoolAppID("sales", 10)
	if err := p.RegisterReplica("sales", 0, backend.URL, nil, 1); err != nil {
		t.Fatal(err)
	}
	const deadlineIn = 300 * time.Millisecond
	// The access middleware normally attaches the identity; here a header
	// selects it so one front door can serve both the support request and the
	// anonymous positive control. The support identity is published just
	// before its request so the deadline is measured from that moment.
	var support atomic.Pointer[auth.ContextUser]
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Support") == "1" {
			r = r.WithContext(auth.WithUser(r.Context(), support.Load()))
		}
		p.ServeHTTP(w, r)
	}))
	defer front.Close()

	// Positive control: for an anonymous visitor the stream is still flowing
	// well past the support deadline, so a stream that ends early below can
	// only be the deadline at work.
	ctx, cancel := context.WithTimeout(context.Background(), 2*deadlineIn)
	lines, elapsed, err := countLines(ctx, t, front.URL+"/app/sales/", nil)
	cancel()
	if err == nil || elapsed < 2*deadlineIn-50*time.Millisecond || lines < 10 {
		t.Fatalf("control stream: lines=%d elapsed=%v err=%v; expected it to outlive the client's %v cut-off", lines, elapsed, err, 2*deadlineIn)
	}

	cancelled.Store(false)
	supportUser := &auth.ContextUser{ID: 2, Username: "alice", Role: "viewer",
		SupportSession: &auth.SupportSessionContext{ID: "support", ActorID: 1, ActorUsername: "admin",
			AppID: 10, AppSlug: "sales", ExpiresAt: time.Now().Add(deadlineIn)}}
	support.Store(supportUser)
	ctx, cancel = context.WithTimeout(context.Background(), 4*deadlineIn)
	defer cancel()
	lines, elapsed, err = countLines(ctx, t, front.URL+"/app/sales/", supportUser)
	if ctx.Err() != nil {
		t.Fatalf("support stream still open %v after a %v session deadline (lines=%d err=%v)", elapsed, deadlineIn, lines, err)
	}
	if elapsed < deadlineIn-50*time.Millisecond || lines < 5 {
		t.Fatalf("support stream ended after %v with %d lines; expected it to flow until the %v deadline", elapsed, lines, deadlineIn)
	}
	deadline := time.Now().Add(time.Second)
	for !cancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cancelled.Load() {
		t.Fatal("backend request was not cancelled when the support session ended")
	}
}
