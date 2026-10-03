package data

import (
	"fmt"
	"math"
)

// QuotaError is returned by QuotaCheck when the projected size exceeds the
// configured quota. Handlers map this to HTTP 413.
type QuotaError struct {
	QuotaBytes     int64
	UsedBytes      int64
	WouldBeBytes   int64
	RemainingBytes int64
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("quota exceeded: would use %d of %d bytes", e.WouldBeBytes, e.QuotaBytes)
}

// ProjectedSize returns the on-disk total after replacing a file of
// existingDestSize with incoming bytes (existingDestSize=0 for new files).
// Totals beyond int64 are reported as math.MaxInt64 rather than wrapping.
func ProjectedSize(used, existingDestSize, incoming int64) int64 {
	base := used - existingDestSize
	if incoming > 0 && base > math.MaxInt64-incoming {
		return math.MaxInt64
	}
	return base + incoming
}

// QuotaCheck returns nil when the projected size fits inside quotaBytes.
// quotaBytes <= 0 disables the check.
func QuotaCheck(used, existingDestSize, incoming, quotaBytes int64) error {
	if quotaBytes <= 0 {
		return nil
	}
	proj := ProjectedSize(used, existingDestSize, incoming)
	remaining := quotaBytes - used + existingDestSize
	// Compare against the space left: adding a client-supplied length to
	// used can overflow, including when the quota itself is MaxInt64.
	if incoming > remaining {
		return &QuotaError{
			QuotaBytes:     quotaBytes,
			UsedBytes:      used,
			WouldBeBytes:   proj,
			RemainingBytes: remaining,
		}
	}
	return nil
}
