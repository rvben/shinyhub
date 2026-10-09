package auth

import (
	"sync"
	"time"
	"unicode/utf8"
)

const (
	forwardAuthEncodingWarningInterval = 5 * time.Minute
	forwardAuthEncodingWarningEntries  = 1024
)

type encodingWarningKey struct {
	peer   string
	header string
}

// encodingWarnings permits recurring diagnostics without retaining unbounded
// peer state. At capacity, new peers share an overflow budget; tracked peers
// retain their own budget. Only authenticated proxy assertions use this limiter.
// The zero value is ready to use.
type encodingWarnings struct {
	mu           sync.Mutex
	last         map[encodingWarningKey]time.Time
	overflowLast time.Time
	sweepAfter   time.Time
}

func (l *encodingWarnings) allow(peer, header string, now time.Time) (allowed, overflow bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := encodingWarningKey{peer: peer, header: header}
	if last, exists := l.last[key]; exists {
		if now.Sub(last) < forwardAuthEncodingWarningInterval {
			return false, false
		}
		l.last[key] = now
		return true, false
	}
	if len(l.last) >= forwardAuthEncodingWarningEntries {
		// Expired budgets can be safely discarded: their next assertion is due
		// another warning. Remember the earliest remaining expiry so overflow
		// traffic cannot force a full-table scan on every request.
		if !now.Before(l.sweepAfter) {
			l.sweepAfter = now.Add(forwardAuthEncodingWarningInterval)
			for key, last := range l.last {
				expires := last.Add(forwardAuthEncodingWarningInterval)
				if !now.Before(expires) {
					delete(l.last, key)
				} else if expires.Before(l.sweepAfter) {
					l.sweepAfter = expires
				}
			}
		}
		if len(l.last) >= forwardAuthEncodingWarningEntries {
			if !l.overflowLast.IsZero() && now.Sub(l.overflowLast) < forwardAuthEncodingWarningInterval {
				return false, true
			}
			l.overflowLast = now
			return true, true
		}
	}
	if l.last == nil {
		l.last = make(map[encodingWarningKey]time.Time)
	}
	l.last[key] = now
	return true, false
}

// Inspect every wire value, including repeated singleton identity fields. A
// valid first value must not hide malformed bytes in a later assertion.
func validUTF8HeaderValues(values []string) bool {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}
