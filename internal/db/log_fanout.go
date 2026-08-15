package db

import (
	"context"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/logstream"
)

type appLogWindowReader func(offset int64) ([]byte, int64, int64, error)

// appLogFanout owns the single steady-state database follower for one run.
// Subscribers read immutable copies from its bounded history, so a slow client
// never stalls polling or delivery to another client.
type appLogFanout struct {
	read           appLogWindowReader
	metrics        AppLogMetrics
	pollInterval   time.Duration
	retentionBytes int64
	wake           chan struct{}
	stopped        chan struct{}
	done           chan struct{}
	stopOnce       sync.Once

	mu          sync.Mutex
	history     []appLogFanoutRecord
	latest      int64
	subscribers map[*appLogSubscriber]struct{}
}

type appLogFanoutRecord struct {
	start  int64
	record logstream.Record
}

type appLogSubscriber struct {
	wake chan struct{}
}

func newAppLogFanout(read appLogWindowReader, metrics AppLogMetrics, offset int64, pollInterval time.Duration, retentionBytes int64) *appLogFanout {
	return &appLogFanout{
		read:           read,
		metrics:        metrics,
		pollInterval:   pollInterval,
		retentionBytes: retentionBytes,
		wake:           make(chan struct{}, 1),
		stopped:        make(chan struct{}),
		done:           make(chan struct{}),
		latest:         offset,
		subscribers:    make(map[*appLogSubscriber]struct{}),
	}
}

func (f *appLogFanout) run() {
	defer close(f.done)
	if f.metrics != nil {
		f.metrics.AddAppLogFollowers(1)
		defer f.metrics.AddAppLogFollowers(-1)
	}
	ticker := time.NewTicker(f.pollInterval)
	defer ticker.Stop()
	for {
		f.poll()
		select {
		case <-f.stopped:
			return
		case <-f.wake:
		case <-ticker.C:
		}
	}
}

func (f *appLogFanout) poll() {
	f.mu.Lock()
	offset := f.latest
	f.mu.Unlock()

	data, start, end, err := f.read(offset)
	if err != nil {
		if f.metrics != nil {
			f.metrics.RecordAppLogFollowError()
		}
		return
	}
	if len(data) == 0 {
		return
	}
	records := logstream.RecordsFromBytes(data, start)
	if len(records) == 0 {
		return
	}

	f.mu.Lock()
	// A remote writer can advance retention between polls. Anything before the
	// returned start is no longer a valid replay source.
	if start > offset {
		f.history = nil
	}
	recordStart := start
	for _, record := range records {
		f.history = append(f.history, appLogFanoutRecord{start: recordStart, record: record})
		recordStart = record.EndOffset
	}
	f.latest = end
	f.trimHistoryLocked()
	for subscriber := range f.subscribers {
		signalAppLogSubscriber(subscriber)
	}
	f.mu.Unlock()
}

func (f *appLogFanout) trimHistoryLocked() {
	if f.retentionBytes <= 0 {
		return
	}
	// Keep a whole oldest record even when a single line exceeds the byte cap;
	// splitting it would make the displayed line and SSE cursor disagree.
	for len(f.history) > 1 && f.latest-f.history[0].record.EndOffset >= f.retentionBytes {
		f.history = f.history[1:]
	}
}

func (f *appLogFanout) addSubscriber() *appLogSubscriber {
	subscriber := &appLogSubscriber{wake: make(chan struct{}, 1)}
	f.mu.Lock()
	f.subscribers[subscriber] = struct{}{}
	if f.metrics != nil {
		f.metrics.AddAppLogViewers(1)
	}
	signalAppLogSubscriber(subscriber)
	f.mu.Unlock()
	return subscriber
}

func (f *appLogFanout) removeSubscriber(subscriber *appLogSubscriber) int {
	f.mu.Lock()
	if _, ok := f.subscribers[subscriber]; ok {
		delete(f.subscribers, subscriber)
		if f.metrics != nil {
			f.metrics.AddAppLogViewers(-1)
		}
	}
	remaining := len(f.subscribers)
	f.mu.Unlock()
	return remaining
}

func (f *appLogFanout) recordsAfter(offset int64) []logstream.Record {
	f.mu.Lock()
	defer f.mu.Unlock()

	first := 0
	for first < len(f.history) && f.history[first].record.EndOffset <= offset {
		first++
	}
	if first == len(f.history) {
		return nil
	}
	records := make([]logstream.Record, len(f.history)-first)
	for i := first; i < len(f.history); i++ {
		records[i-first] = f.history[i].record
	}
	if offset < f.history[first].start {
		records[0].GapBefore = true
	}
	return records
}

func (f *appLogFanout) signal() {
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *appLogFanout) stop() {
	f.stopOnce.Do(func() { close(f.stopped) })
}

func signalAppLogSubscriber(subscriber *appLogSubscriber) {
	select {
	case subscriber.wake <- struct{}{}:
	default:
	}
}

func (s *Store) subscribeAppLog(runID string, offset int64) (*appLogFanout, *appLogSubscriber, bool) {
	s.appLogFanoutsMu.Lock()
	defer s.appLogFanoutsMu.Unlock()
	if s.appLogFanoutsClosed {
		return nil, nil, false
	}
	if s.appLogFanouts == nil {
		s.appLogFanouts = make(map[string]*appLogFanout)
	}
	fanout := s.appLogFanouts[runID]
	if fanout == nil {
		fanout = newAppLogFanout(
			func(cursor int64) ([]byte, int64, int64, error) {
				return s.ReadAppLogWindow(runID, cursor)
			},
			s.appLogMetricsRecorder(),
			offset,
			appLogFlushInterval,
			AppLogRetentionBytes,
		)
		s.appLogFanouts[runID] = fanout
		s.appLogFanoutsWG.Add(1)
		go func() {
			defer s.appLogFanoutsWG.Done()
			fanout.run()
		}()
	}
	return fanout, fanout.addSubscriber(), true
}

func (s *Store) unsubscribeAppLog(runID string, fanout *appLogFanout, subscriber *appLogSubscriber) {
	s.appLogFanoutsMu.Lock()
	defer s.appLogFanoutsMu.Unlock()
	if fanout.removeSubscriber(subscriber) != 0 || s.appLogFanouts[runID] != fanout {
		return
	}
	delete(s.appLogFanouts, runID)
	fanout.stop()
}

func (s *Store) wakeAppLogFanout(runID string) {
	s.appLogFanoutsMu.Lock()
	fanout := s.appLogFanouts[runID]
	s.appLogFanoutsMu.Unlock()
	if fanout != nil {
		fanout.signal()
	}
}

func (s *Store) followAppLogFanout(ctx context.Context, runID string, offset int64, records chan<- logstream.Record) {
	fanout, subscriber, ok := s.subscribeAppLog(runID, offset)
	if !ok {
		return
	}
	defer s.unsubscribeAppLog(runID, fanout, subscriber)
	fanout.follow(ctx, subscriber, offset, records)
}

func (f *appLogFanout) follow(ctx context.Context, subscriber *appLogSubscriber, offset int64, records chan<- logstream.Record) {
	for {
		available := f.recordsAfter(offset)
		if len(available) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-f.stopped:
				return
			case <-subscriber.wake:
				continue
			}
		}
		for _, record := range available {
			select {
			case records <- record:
				offset = record.EndOffset
			case <-ctx.Done():
				return
			case <-f.stopped:
				return
			}
		}
	}
}
