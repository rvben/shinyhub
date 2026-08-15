// Package logstream defines the cursor-bearing records shared by local-file
// and database-backed application log readers.
package logstream

import (
	"bytes"
	"strings"
)

// StreamState describes the health of live retained-log delivery.
type StreamState string

const (
	StreamDegraded  StreamState = "degraded"
	StreamRecovered StreamState = "recovered"
)

// Record is one display line and the exclusive byte offset immediately after
// it. EndOffset is suitable for an SSE id and resuming with Last-Event-ID.
type Record struct {
	Line      string
	EndOffset int64
	// StreamState carries cursor-free delivery health transitions. These are
	// operational SSE events, not application output, and never advance the
	// durable resume cursor.
	StreamState StreamState
	// GapBefore reports that the requested resume cursor predates the earliest
	// available byte. Consumers should surface this once before the record.
	GapBefore bool
}

// RecordsFromBytes splits retained bytes into display lines while preserving
// their absolute end offsets. A final unterminated line is included so crash
// output without a trailing newline remains visible.
func RecordsFromBytes(data []byte, startOffset int64) []Record {
	if len(data) == 0 {
		return nil
	}
	records := make([]Record, 0, bytes.Count(data, []byte{'\n'})+1)
	lineStart := 0
	for i, b := range data {
		if b != '\n' {
			continue
		}
		line := strings.TrimSuffix(string(data[lineStart:i]), "\r")
		records = append(records, Record{Line: line, EndOffset: startOffset + int64(i+1)})
		lineStart = i + 1
	}
	if lineStart < len(data) {
		line := strings.TrimSuffix(string(data[lineStart:]), "\r")
		records = append(records, Record{Line: line, EndOffset: startOffset + int64(len(data))})
	}
	return records
}
