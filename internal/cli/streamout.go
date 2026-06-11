package cli

import (
	"encoding/json"
	"fmt"
	"io"
)

// streamWriter renders one log line per write: raw passthrough in table
// mode, one NDJSON object per line in ndjson mode. replica 0 means "not
// replica-scoped" and is omitted from the JSON object.
type streamWriter struct {
	w       io.Writer
	format  outputFormat
	replica int
}

func newStreamWriter(w io.Writer, format outputFormat, replica int) *streamWriter {
	return &streamWriter{w: w, format: format, replica: replica}
}

func (s *streamWriter) WriteLine(line string) {
	if s.format != formatNDJSON {
		fmt.Fprintln(s.w, line)
		return
	}
	obj := map[string]any{"line": line}
	if s.replica != 0 {
		obj["replica"] = s.replica
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return
	}
	fmt.Fprintln(s.w, string(b))
}
