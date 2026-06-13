package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// streamWriter renders one log line per write: raw passthrough in table
// mode, one NDJSON object per line in ndjson mode. A nil replica means the
// stream is not replica-scoped (e.g. schedule run logs) and the "replica" key
// is omitted; a non-nil replica is a real replica index (including 0, the
// default first replica of `apps logs`) and is always emitted, so every line
// of a replica-scoped stream carries a consistent schema.
type streamWriter struct {
	w       io.Writer
	format  outputFormat
	replica *int
}

func newStreamWriter(w io.Writer, format outputFormat, replica *int) *streamWriter {
	return &streamWriter{w: w, format: format, replica: replica}
}

func (s *streamWriter) WriteLine(line string) {
	if s.format != formatNDJSON {
		fmt.Fprintln(s.w, line)
		return
	}
	obj := map[string]any{"line": line}
	if s.replica != nil {
		obj["replica"] = *s.replica
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return
	}
	fmt.Fprintln(s.w, string(b))
}
