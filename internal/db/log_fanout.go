package db

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/logstream"
)

type appLogWindowReader func(offset int64) ([]byte, int64, int64, error)

const appLogFollowMaxBackoff = 5 * time.Second

// appLogFanout owns the single steady-state database follower for one run.
// Subscribers read immutable copies from its bounded history, so a slow client
// never stalls polling or delivery to another client.
type appLogFanout struct {
	read           appLogWindowReader
	metrics        AppLogMetrics
	pollInterval   time.Duration
	retryDelay     func(consecutiveFailures int) time.Duration
	retentionBytes int64
	wake           chan struct{}
	stopped        chan struct{}
	done           chan struct{}
	stopOnce       sync.Once

	mu           sync.Mutex
	history      []appLogFanoutRecord
	latest       int64
	streamState  logstream.StreamState
	stateVersion uint64
	subscribers  map[*appLogSubscriber]struct{}
}

type appLogFanoutRecord struct {
	start  int64
	record logstream.Record
}

type appLogSubscriber struct {
	wake             chan struct{}
	seenStateVersion uint64
}

func newAppLogFanout(read appLogWindowReader, metrics AppLogMetrics, offset int64, pollInterval time.Duration, retentionBytes int64) *appLogFanout {
	return &appLogFanout{
		read:         read,
		metrics:      metrics,
		pollInterval: pollInterval,
		retryDelay: func(consecutiveFailures int) time.Duration {
			return appLogFollowBackoff(pollInterval, appLogFollowMaxBackoff, consecutiveFailures, rand.Float64())
		},
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
	consecutiveFailures := 0
	for {
		hadRecords, err := f.poll()
		delay := f.pollInterval
		if err != nil {
			if consecutiveFailures == 0 {
				f.setStreamState(logstream.StreamDegraded)
			}
			consecutiveFailures++
			delay = f.retryDelay(consecutiveFailures)
		} else {
			if consecutiveFailures > 0 {
				f.setStreamState(logstream.StreamRecovered)
			}
			consecutiveFailures = 0
			if hadRecords {
				f.signalSubscribers()
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-f.stopped:
			stopAppLogTimer(timer)
			return
		case <-f.wake:
			stopAppLogTimer(timer)
		case <-timer.C:
		}
	}
}

func appLogFollowBackoff(base, maximum time.Duration, consecutiveFailures int, sample float64) time.Duration {
	if base <= 0 || consecutiveFailures <= 0 {
		return base
	}
	// A missing ceiling falls back to the base cadence.
	if maximum <= 0 {
		maximum = base
	}
	if maximum <= base {
		return maximum
	}
	delay := base
	for range consecutiveFailures {
		if delay >= maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if sample < 0 {
		sample = 0
	} else if sample > 1 {
		sample = 1
	}
	// ±20% jitter prevents every run and control-plane node from retrying a
	// recovering database in lockstep.
	delay = time.Duration(float64(delay) * (0.8 + 0.4*sample))
	if delay > maximum {
		return maximum
	}
	return delay
}

func stopAppLogTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (f *appLogFanout) poll() (bool, error) {
	f.mu.Lock()
	offset := f.latest
	f.mu.Unlock()

	data, start, end, err := f.read(offset)
	if err != nil {
		if f.metrics != nil {
			f.metrics.RecordAppLogFollowError()
		}
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	records := logstream.RecordsFromBytes(data, start)
	if len(records) == 0 {
		return false, nil
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
	f.mu.Unlock()
	return true, nil
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
	f.mu.Lock()
	subscriber := &appLogSubscriber{
		wake:             make(chan struct{}, 1),
		seenStateVersion: f.stateVersion,
	}
	// A viewer joining during an incident needs the current degraded state;
	// stale recovery events are intentionally not replayed to later viewers.
	if f.streamState == logstream.StreamDegraded && subscriber.seenStateVersion > 0 {
		subscriber.seenStateVersion--
	}
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
	return f.recordsAfterLocked(offset)
}

func (f *appLogFanout) nextRecords(subscriber *appLogSubscriber, offset int64) []logstream.Record {
	f.mu.Lock()
	defer f.mu.Unlock()

	records := f.recordsAfterLocked(offset)
	if subscriber.seenStateVersion == f.stateVersion || f.streamState == "" {
		return records
	}
	subscriber.seenStateVersion = f.stateVersion
	return append([]logstream.Record{{StreamState: f.streamState}}, records...)
}

func (f *appLogFanout) recordsAfterLocked(offset int64) []logstream.Record {

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

func (f *appLogFanout) setStreamState(state logstream.StreamState) {
	f.mu.Lock()
	if f.streamState == state {
		f.mu.Unlock()
		return
	}
	f.streamState = state
	f.stateVersion++
	for subscriber := range f.subscribers {
		signalAppLogSubscriber(subscriber)
	}
	f.mu.Unlock()
}

func (f *appLogFanout) signalSubscribers() {
	f.mu.Lock()
	for subscriber := range f.subscribers {
		signalAppLogSubscriber(subscriber)
	}
	f.mu.Unlock()
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
		available := f.nextRecords(subscriber, offset)
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
				if record.StreamState == "" {
					offset = record.EndOffset
				}
			case <-ctx.Done():
				return
			case <-f.stopped:
				return
			}
		}
	}
}
