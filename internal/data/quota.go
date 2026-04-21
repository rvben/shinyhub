package data

import "fmt"

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
func ProjectedSize(used, existingDestSize, incoming int64) int64 {
	return used - existingDestSize + incoming
}

// QuotaCheck returns nil when the projected size fits inside quotaBytes.
// quotaBytes <= 0 disables the check.
func QuotaCheck(used, existingDestSize, incoming, quotaBytes int64) error {
	if quotaBytes <= 0 {
		return nil
	}
	proj := ProjectedSize(used, existingDestSize, incoming)
	if proj > quotaBytes {
		return &QuotaError{
			QuotaBytes:     quotaBytes,
			UsedBytes:      used,
			WouldBeBytes:   proj,
			RemainingBytes: quotaBytes - used + existingDestSize,
		}
	}
	return nil
}
