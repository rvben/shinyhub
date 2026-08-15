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

	fanout := newAppLogFanout(read, 0, time.Hour, AppLogRetentionBytes)
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
}

func TestAppLogFanoutMarksSubscriberThatFallsBehindHistory(t *testing.T) {
	fanout := newAppLogFanout(nil, 0, time.Hour, 8)
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
