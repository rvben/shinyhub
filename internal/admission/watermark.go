package admission

import (
	"context"
	"sync"
	"time"
)

// watermarkStale is how old a reading may be before it is treated as no reading
// at all. Freshness is measured from capture time, so a dead sampler cannot
// freeze admissions at a stale value.
const watermarkStale = 5 * time.Second

// watermarkInterval is the background sampling cadence.
const watermarkInterval = 1 * time.Second

// Watermark sheds new admissions when host CPU is at or above maxPercent. It
// samples on a background goroutine into a mutex-guarded reading plus its
// capture time, so the admission path reads without blocking. It fails open on
// every uncertainty: disabled, no reading yet, an errored probe, or a stale
// reading. A missing signal must never read as "0% busy".
type Watermark struct {
	maxPercent float64
	sample     func() (float64, error)
	nowFn      func() time.Time

	mu       sync.Mutex
	pct      float64
	capAt    time.Time
	hasValue bool
}

// NewWatermark builds a watermark. maxPercent <= 0 disables it (Admit always
// true). sample returns instantaneous host CPU busy percent; it may block, so it
// is only ever called from Run's goroutine, never from Admit.
func NewWatermark(maxPercent float64, sample func() (float64, error)) *Watermark {
	return &Watermark{
		maxPercent: maxPercent,
		sample:     sample,
		nowFn:      time.Now,
	}
}

// record stores a reading with its capture time.
func (w *Watermark) record(pct float64, at time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pct = pct
	w.capAt = at
	w.hasValue = true
}

// Admit reports whether a new admission is allowed. It fails open on every
// uncertain case so a broken or absent signal never takes the platform down.
func (w *Watermark) Admit() bool {
	if w.maxPercent <= 0 {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.hasValue {
		return true
	}
	if w.nowFn().Sub(w.capAt) > watermarkStale {
		return true
	}
	return w.pct < w.maxPercent
}

// Run samples host CPU every watermarkInterval until ctx is cancelled. Errored
// samples are skipped, not recorded, so a transient probe failure leaves the
// last good reading to age out via the staleness horizon rather than being read
// as zero.
func (w *Watermark) Run(ctx context.Context) {
	if w.maxPercent <= 0 {
		return
	}
	ticker := time.NewTicker(watermarkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if pct, err := w.sample(); err == nil {
				w.record(pct, w.nowFn())
			}
		}
	}
}
