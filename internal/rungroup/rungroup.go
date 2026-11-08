// Package rungroup owns the server's long-lived background services. Each
// service runs under its own cancelable context, carries a name and a stop
// budget, and is joined when stopped, so shutdown can say which service it is
// waiting for and how long it may wait. Services are stopped individually at
// the point the shutdown sequence needs them gone, and Group.Stop stops
// whatever is left in reverse start order: a service started later may depend
// on one started earlier, never the reverse.
package rungroup

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/safego"
)

// Group tracks the services started through Go.
type Group struct {
	logger *slog.Logger

	mu       sync.Mutex
	services []*Service
	stopped  bool
}

// New returns an empty group that logs through logger.
func New(logger *slog.Logger) *Group {
	return &Group{logger: logger}
}

// Service is one running background function.
type Service struct {
	name   string
	budget time.Duration
	logger *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Context returns the context the service runs under. Work the service starts
// on another goroutine, such as a request-driven sync, can bind to it so Stop
// ends that work too.
func (s *Service) Context() context.Context {
	return s.ctx
}

// Done is closed once the service has returned. A caller that must not
// proceed while the service is still running, even past its budget, waits on
// it after a Stop that reported an overrun.
func (s *Service) Done() <-chan struct{} {
	return s.done
}

// Go starts run in a new goroutine under a fresh context that Stop cancels.
// budget is how long a stop waits for run to return after cancellation. A
// panic in run is logged and ends the service; a safego.Fatal panic still
// crashes the process. Once the group has stopped, Go starts nothing and
// returns a service that is already stopped.
func (g *Group) Go(name string, budget time.Duration, run func(ctx context.Context)) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{name: name, budget: budget, logger: g.logger, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	g.mu.Lock()
	if g.stopped {
		g.mu.Unlock()
		cancel()
		close(s.done)
		return s
	}
	g.services = append(g.services, s)
	g.mu.Unlock()
	go func() {
		defer close(s.done)
		defer func() {
			if r := recover(); r != nil {
				safego.RepanicFatal(r)
				s.logger.Error("background service panicked", "service", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		run(ctx)
	}()
	return s
}

// Stop cancels the service and waits up to its budget for it to return. It
// reports whether the service has exited. Stop is safe to call more than
// once and from several goroutines; every caller waits for the same exit.
func (s *Service) Stop() bool {
	s.cancel()
	select {
	case <-s.done:
		return true
	default:
	}
	start := time.Now()
	timer := time.NewTimer(s.budget)
	defer timer.Stop()
	select {
	case <-s.done:
		if elapsed := time.Since(start); elapsed > s.budget/2 {
			s.logger.Info("background service stopped slowly", "service", s.name, "elapsed", elapsed, "budget", s.budget)
		}
		return true
	case <-timer.C:
		s.logger.Error("background service still running after its stop budget; continuing shutdown without it",
			"service", s.name, "budget", s.budget)
		return false
	}
}

// Stop prevents new services from starting and stops every service in reverse
// start order, each bounded by its own budget. It returns the names of the
// services that were still running when their budget ran out.
func (g *Group) Stop() []string {
	g.mu.Lock()
	g.stopped = true
	services := append([]*Service(nil), g.services...)
	g.mu.Unlock()
	var overran []string
	for i := len(services) - 1; i >= 0; i-- {
		if !services[i].Stop() {
			overran = append(overran, services[i].name)
		}
	}
	return overran
}

// Plan returns the longest Stop can take: the sum of every started service's
// budget.
func (g *Group) Plan() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	var total time.Duration
	for _, s := range g.services {
		total += s.budget
	}
	return total
}
