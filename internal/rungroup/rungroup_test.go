package rungroup

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestGroup() (*Group, *logBuffer) {
	buf := &logBuffer{}
	return New(slog.New(slog.NewTextHandler(buf, nil))), buf
}

// untilCancelled is a service body that records its name once its context is
// cancelled, so the order of the record is the order of the stops.
func untilCancelled(name string, mu *sync.Mutex, order *[]string) func(context.Context) {
	return func(ctx context.Context) {
		<-ctx.Done()
		mu.Lock()
		*order = append(*order, name)
		mu.Unlock()
	}
}

func TestStopJoinsServicesInReverseStartOrder(t *testing.T) {
	g, _ := newTestGroup()
	var mu sync.Mutex
	var order []string
	for _, name := range []string{"first", "second", "third"} {
		g.Go(name, time.Second, untilCancelled(name, &mu, &order))
	}
	if overran := g.Stop(); len(overran) != 0 {
		t.Fatalf("Stop reported overruns %v for services that stop at once", overran)
	}
	want := []string{"third", "second", "first"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("stop order = %v, want %v", order, want)
	}
}

func TestServiceStopIsIdempotentAndJoinsConcurrentCallers(t *testing.T) {
	g, _ := newTestGroup()
	release := make(chan struct{})
	exited := make(chan struct{})
	svc := g.Go("slow", time.Minute, func(ctx context.Context) {
		<-ctx.Done()
		<-release
		close(exited)
	})
	var wg sync.WaitGroup
	returned := make(chan struct{}, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.Stop()
			select {
			case <-exited:
			default:
				t.Error("Service.Stop returned before the service exited")
			}
			returned <- struct{}{}
		}()
	}
	select {
	case <-returned:
		t.Fatal("a Stop caller returned while the service was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if !svc.Stop() {
		t.Fatal("a repeated Stop on an exited service must report it stopped in budget")
	}
}

func TestGroupStopMovesOnPastAServiceThatOverrunsItsBudget(t *testing.T) {
	g, logs := newTestGroup()
	var mu sync.Mutex
	var order []string
	release := make(chan struct{})
	defer close(release)
	g.Go("before", time.Second, untilCancelled("before", &mu, &order))
	g.Go("stuck", 20*time.Millisecond, func(ctx context.Context) {
		<-ctx.Done()
		<-release
	})
	start := time.Now()
	overran := g.Stop()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Stop waited %s for a service whose budget is 20ms", elapsed)
	}
	if !reflect.DeepEqual(overran, []string{"stuck"}) {
		t.Fatalf("overran = %v, want [stuck]", overran)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []string{"before"}) {
		t.Fatalf("services after the stuck one were not stopped: %v", order)
	}
	if out := logs.String(); !strings.Contains(out, "service=stuck") || !strings.Contains(out, "still running") {
		t.Fatalf("overrun not logged with the service name: %q", out)
	}
}

func TestPanickingServiceIsRecoveredAndCountsAsStopped(t *testing.T) {
	g, logs := newTestGroup()
	svc := g.Go("explodes", time.Second, func(context.Context) { panic("kaboom") })
	if !svc.Stop() {
		t.Fatal("a service that panicked has exited and must report stopped in budget")
	}
	if out := logs.String(); !strings.Contains(out, "kaboom") || !strings.Contains(out, "service=explodes") {
		t.Fatalf("panic not logged with its value and service name: %q", out)
	}
}

func TestGoAfterStopNeverRunsTheService(t *testing.T) {
	g, _ := newTestGroup()
	g.Stop()
	ran := make(chan struct{}, 1)
	svc := g.Go("late", time.Second, func(context.Context) { ran <- struct{}{} })
	if !svc.Stop() {
		t.Fatal("a service refused after Stop has nothing running and must report stopped")
	}
	select {
	case <-ran:
		t.Fatal("Go after Group.Stop ran the service")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStopOnlyStopsServicesStillRunning(t *testing.T) {
	g, _ := newTestGroup()
	var calls int
	var mu sync.Mutex
	svc := g.Go("once", time.Second, func(ctx context.Context) {
		<-ctx.Done()
		mu.Lock()
		calls++
		mu.Unlock()
	})
	svc.Stop()
	if overran := g.Stop(); len(overran) != 0 {
		t.Fatalf("overran = %v after the only service already stopped", overran)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("service body ran its exit %d times, want 1", calls)
	}
}

func TestServiceContextIsCancelledByStop(t *testing.T) {
	g, _ := newTestGroup()
	svc := g.Go("loop", time.Second, func(ctx context.Context) { <-ctx.Done() })
	if err := svc.Context().Err(); err != nil {
		t.Fatalf("context of a running service is already done: %v", err)
	}
	svc.Stop()
	if svc.Context().Err() == nil {
		t.Fatal("Stop left the service context live for work started outside the service")
	}
}

func TestPlanIsTheSumOfBudgets(t *testing.T) {
	g, _ := newTestGroup()
	g.Go("a", 2*time.Second, func(ctx context.Context) { <-ctx.Done() })
	g.Go("b", 3*time.Second, func(ctx context.Context) { <-ctx.Done() })
	defer g.Stop()
	if got := g.Plan(); got != 5*time.Second {
		t.Fatalf("Plan() = %s, want 5s", got)
	}
}
