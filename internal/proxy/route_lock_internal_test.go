package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// routeLockPausingWriter pauses ServeHTTP at the sticky-cookie write between
// replica selection and the activeConns increment.
type routeLockPausingWriter struct {
	*httptest.ResponseRecorder
	once    sync.Once
	paused  chan struct{}
	release chan struct{}
}

func (w *routeLockPausingWriter) Header() http.Header {
	w.once.Do(func() {
		close(w.paused)
		<-w.release
	})
	return w.ResponseRecorder.Header()
}

// TestProxy_ServeHTTP_HoldsRouteLockThroughActiveConnsBump verifies the two
// event-driven halves of the hibernation invariant: the route read lock is held
// until activeConns is incremented, then activeConns prevents hibernation while
// the backend request remains in flight.
func TestProxy_ServeHTTP_HoldsRouteLockThroughActiveConnsBump(t *testing.T) {
	backendEntered := make(chan struct{})
	backendRelease := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(backendEntered)
		<-backendRelease
		w.WriteHeader(http.StatusOK)
	}))
	defer func() {
		select {
		case <-backendRelease:
		default:
			close(backendRelease)
		}
		backend.Close()
	}()

	p := New()
	if err := p.Register("app", backend.URL); err != nil {
		t.Fatal(err)
	}

	w := &routeLockPausingWriter{
		ResponseRecorder: httptest.NewRecorder(),
		paused:           make(chan struct{}),
		release:          make(chan struct{}),
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		req := httptest.NewRequest(http.MethodGet, "/app/app/", nil)
		p.ServeHTTP(w, req)
	}()

	select {
	case <-w.paused:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP never reached the sticky-cookie write")
	}

	// A write lock must be unavailable while ServeHTTP is paused before the
	// activeConns bump. TryLock makes this assertion immediate and independent
	// of goroutine scheduling or an arbitrary timeout.
	if p.mu.TryLock() {
		p.mu.Unlock()
		t.Fatal("route write lock was available before activeConns was incremented")
	}

	close(w.release)
	select {
	case <-backendEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never received the in-flight request")
	}

	// The route read lock is released once the request is accounted for, but
	// the live activeConns value must still make hibernation fail.
	if p.BeginHibernate("app", time.Now().Add(time.Hour)) {
		t.Error("BeginHibernate returned true while the backend request was in flight")
	}
	if !p.HasLiveReplica("app") {
		t.Error("pool was removed despite an in-flight request")
	}

	close(backendRelease)
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not complete after the backend was released")
	}
}
