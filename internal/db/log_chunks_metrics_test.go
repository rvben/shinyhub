package db

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type appLogMetricsSpy struct {
	mu      sync.Mutex
	flushes []string
	pending int64
	dropped int64
	lags    []time.Duration
}

func (m *appLogMetricsSpy) RecordAppLogFlush(result string, _ time.Duration, lag time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flushes = append(m.flushes, result)
	if result == "ok" {
		m.lags = append(m.lags, lag)
	}
}

func (m *appLogMetricsSpy) AddAppLogPendingBytes(delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending += delta
}

func (m *appLogMetricsSpy) RecordAppLogDroppedBytes(bytes int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped += bytes
}

func TestAppLogWriterMetricsTrackFailureAndRecovery(t *testing.T) {
	metrics := &appLogMetricsSpy{}
	attempts := 0
	w := &AppLogWriter{
		appendChunk: func(_ string, _, _ int64, _ []byte, _ int64, _ time.Time) error {
			attempts++
			if attempts == 1 {
				return errors.New("database unavailable")
			}
			return nil
		},
		metrics:  metrics,
		runID:    "run-1",
		maxBytes: AppLogRetentionBytes,
	}

	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if had, err := w.flushOne(); !had || err == nil {
		t.Fatalf("first flush = had:%v err:%v, want attempted error", had, err)
	}
	if metrics.pending != 5 || metrics.dropped != 0 {
		t.Fatalf("after retryable failure pending=%d dropped=%d, want 5/0", metrics.pending, metrics.dropped)
	}
	if had, err := w.flushOne(); !had || err != nil {
		t.Fatalf("recovery flush = had:%v err:%v, want success", had, err)
	}
	if metrics.pending != 0 || metrics.dropped != 0 {
		t.Fatalf("after recovery pending=%d dropped=%d, want 0/0", metrics.pending, metrics.dropped)
	}
	if len(metrics.flushes) != 2 || metrics.flushes[0] != "error" || metrics.flushes[1] != "ok" {
		t.Fatalf("flush results = %v, want [error ok]", metrics.flushes)
	}
	if len(metrics.lags) != 1 || metrics.lags[0] < 0 {
		t.Fatalf("successful persistence lags = %v, want one non-negative observation", metrics.lags)
	}
}

func TestAppLogWriterMetricsAccountForBoundedAndTerminalLoss(t *testing.T) {
	metrics := &appLogMetricsSpy{}
	done := make(chan struct{})
	close(done)
	w := &AppLogWriter{
		appendChunk: func(_ string, _, _ int64, _ []byte, _ int64, _ time.Time) error {
			return errors.New("database unavailable")
		},
		metrics:  metrics,
		runID:    "run-1",
		maxBytes: 4,
		stop:     make(chan struct{}),
		done:     done,
	}

	if _, err := w.Write([]byte("abcdefghij")); err != nil {
		t.Fatal(err)
	}
	if metrics.pending != 4 || metrics.dropped != 6 {
		t.Fatalf("after bounded write pending=%d dropped=%d, want 4/6", metrics.pending, metrics.dropped)
	}
	if err := w.Close(); err == nil {
		t.Fatal("Close succeeded, want final persistence error")
	}
	if metrics.pending != 0 || metrics.dropped != 10 {
		t.Fatalf("after terminal failure pending=%d dropped=%d, want 0/10", metrics.pending, metrics.dropped)
	}
}
