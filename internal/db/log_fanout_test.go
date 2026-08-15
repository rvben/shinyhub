package db

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/logstream"
)

func TestAppLogFanoutSharesPollingAndIsolatesSlowSubscribers(t *testing.T) {
	var (
		sourceMu sync.Mutex
		source   []byte
		reads    atomic.Int64
	)
	readObserved := make(chan struct{}, 4)
	read := func(offset int64) ([]byte, int64, int64, error) {
		reads.Add(1)
		select {
		case readObserved <- struct{}{}:
		default:
		}
		sourceMu.Lock()
		defer sourceMu.Unlock()
		end := int64(len(source))
		if offset >= end {
			return nil, offset, offset, nil
		}
		return append([]byte(nil), source[offset:]...), offset, end, nil
	}

	metrics := &appLogMetricsSpy{}
	fanout := newAppLogFanout(read, metrics, 0, time.Hour, AppLogRetentionBytes)
	slowSubscriber := fanout.addSubscriber()
	fastSubscriber := fanout.addSubscriber()
	go fanout.run()
	t.Cleanup(func() {
		fanout.stop()
		<-fanout.done
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	slowOutput := make(chan logstream.Record)
	fastOutput := make(chan logstream.Record, 2)
	go fanout.follow(ctx, slowSubscriber, 0, slowOutput)
	go fanout.follow(ctx, fastSubscriber, 0, fastOutput)

	select {
	case <-readObserved:
	case <-time.After(time.Second):
		t.Fatal("fanout did not perform its initial poll")
	}
	appendSource := func(line string) {
		sourceMu.Lock()
		source = append(source, line...)
		sourceMu.Unlock()
		fanout.signal()
	}
	assertFastLine := func(want string) {
		t.Helper()
		select {
		case got := <-fastOutput:
			if got.Line != want {
				t.Fatalf("fast subscriber line = %q, want %q", got.Line, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("fast subscriber was blocked behind slow subscriber; want %q", want)
		}
	}

	appendSource("first\n")
	assertFastLine("first")
	appendSource("second\n")
	assertFastLine("second")
	if got := reads.Load(); got != 3 {
		t.Fatalf("database reads = %d, want 3 (one initial plus one per append, independent of subscriber count)", got)
	}
	metrics.mu.Lock()
	followers, viewers, followErrors := metrics.followers, metrics.viewers, metrics.followErrors
	metrics.mu.Unlock()
	if followers != 1 || viewers != 2 || followErrors != 0 {
		t.Fatalf("live metrics = followers:%d viewers:%d errors:%d, want 1/2/0", followers, viewers, followErrors)
	}
	fanout.removeSubscriber(slowSubscriber)
	fanout.removeSubscriber(fastSubscriber)
	fanout.stop()
	<-fanout.done
	metrics.mu.Lock()
	followers, viewers = metrics.followers, metrics.viewers
	metrics.mu.Unlock()
	if followers != 0 || viewers != 0 {
		t.Fatalf("stopped metrics = followers:%d viewers:%d, want 0/0", followers, viewers)
	}
}

func TestAppLogFanoutMarksSubscriberThatFallsBehindHistory(t *testing.T) {
	fanout := newAppLogFanout(nil, nil, 0, time.Hour, 8)
	fanout.history = []appLogFanoutRecord{
		{start: 0, record: logstream.Record{Line: "old", EndOffset: 4}},
		{start: 4, record: logstream.Record{Line: "new", EndOffset: 8}},
		{start: 8, record: logstream.Record{Line: "last", EndOffset: 13}},
	}
	fanout.latest = 13
	fanout.trimHistoryLocked()

	got := fanout.recordsAfter(0)
	if len(got) != 2 || got[0].Line != "new" || !got[0].GapBefore {
		t.Fatalf("records after trimmed cursor = %+v", got)
	}
}

func TestAppLogFanoutRecordsPollErrors(t *testing.T) {
	metrics := &appLogMetricsSpy{}
	fanout := newAppLogFanout(
		func(int64) ([]byte, int64, int64, error) {
			return nil, 0, 0, context.DeadlineExceeded
		},
		metrics,
		0,
		time.Hour,
		AppLogRetentionBytes,
	)
	if err := fanout.poll(); err == nil {
		t.Fatal("poll succeeded, want injected error")
	}
	if metrics.followErrors != 1 {
		t.Fatalf("follow errors = %d, want 1", metrics.followErrors)
	}
}

func TestAppLogFollowBackoffIsCappedAndJittered(t *testing.T) {
	const base = 200 * time.Millisecond
	tests := []struct {
		name     string
		failures int
		sample   float64
		maximum  time.Duration
		want     time.Duration
	}{
		{name: "first low jitter", failures: 1, sample: 0, want: 320 * time.Millisecond},
		{name: "first midpoint", failures: 1, sample: 0.5, want: 400 * time.Millisecond},
		{name: "first high jitter", failures: 1, sample: 1, want: 480 * time.Millisecond},
		{name: "eventually capped", failures: 20, sample: 1, want: appLogFollowMaxBackoff},
		{name: "smaller ceiling wins", failures: 1, sample: 0.5, maximum: 100 * time.Millisecond, want: 100 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maximum := tt.maximum
			if maximum == 0 {
				maximum = appLogFollowMaxBackoff
			}
			if got := appLogFollowBackoff(base, maximum, tt.failures, tt.sample); got != tt.want {
				t.Fatalf("backoff = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestAppLogFanoutWakeInterruptsFailureBackoff(t *testing.T) {
	var attempts atomic.Int64
	metrics := &appLogMetricsSpy{}
	fanout := newAppLogFanout(
		func(offset int64) ([]byte, int64, int64, error) {
			if attempts.Add(1) == 1 {
				return nil, offset, offset, context.DeadlineExceeded
			}
			return []byte("recovered\n"), offset, offset + int64(len("recovered\n")), nil
		},
		metrics,
		0,
		time.Hour,
		AppLogRetentionBytes,
	)
	backoffStarted := make(chan int, 1)
	fanout.retryDelay = func(consecutiveFailures int) time.Duration {
		backoffStarted <- consecutiveFailures
		return time.Hour
	}
	subscriber := fanout.addSubscriber()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := make(chan logstream.Record, 1)
	go fanout.follow(ctx, subscriber, 0, output)
	go fanout.run()
	t.Cleanup(func() {
		fanout.stop()
		<-fanout.done
	})

	select {
	case failures := <-backoffStarted:
		if failures != 1 {
			t.Fatalf("consecutive failures = %d, want 1", failures)
		}
	case <-time.After(time.Second):
		t.Fatal("fanout did not enter failure backoff")
	}
	fanout.signal()
	select {
	case got := <-output:
		if got.Line != "recovered" || attempts.Load() != 2 {
			t.Fatalf("recovery = %+v after %d attempts", got, attempts.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("wake did not interrupt follower backoff")
	}
	fanout.removeSubscriber(subscriber)
}
