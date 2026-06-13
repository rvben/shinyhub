package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamWriter_NDJSONWrapsLines(t *testing.T) {
	var out bytes.Buffer
	r := 2
	w := newStreamWriter(&out, formatNDJSON, &r)
	w.WriteLine("app started")
	w.WriteLine(`has "quotes" and {braces}`)
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines", len(lines))
	}
	for _, l := range lines {
		var obj struct {
			Line    string `json:"line"`
			Replica int    `json:"replica"`
		}
		if err := json.Unmarshal([]byte(l), &obj); err != nil {
			t.Fatalf("not NDJSON: %q: %v", l, err)
		}
		if obj.Replica != 2 {
			t.Errorf("replica = %d", obj.Replica)
		}
	}
}

func TestStreamWriter_TablePassthrough(t *testing.T) {
	var out bytes.Buffer
	w := newStreamWriter(&out, formatTable, nil)
	w.WriteLine("raw line")
	if out.String() != "raw line\n" {
		t.Errorf("table mode must pass lines through: %q", out.String())
	}
}

// Replica 0 is a real replica index for `apps logs` (the default, first
// replica), so NDJSON lines must carry "replica":0 — the same schema every
// other replica index gets. Omitting it for index 0 left replica-0 lines
// untagged while replicas 1+ were tagged, breaking cross-replica aggregation.
func TestStreamWriter_NDJSONReplicaZeroTagged(t *testing.T) {
	var out bytes.Buffer
	r := 0
	w := newStreamWriter(&out, formatNDJSON, &r)
	w.WriteLine("replica zero line")
	var obj map[string]any
	if err := json.Unmarshal(bytes.TrimRight(out.Bytes(), "\n"), &obj); err != nil {
		t.Fatalf("not NDJSON: %q: %v", out.String(), err)
	}
	v, has := obj["replica"]
	if !has {
		t.Fatalf("replica key must be present for replica index 0: %q", out.String())
	}
	if v != float64(0) {
		t.Errorf("replica = %v, want 0", v)
	}
}

// A nil replica means "not replica-scoped" (e.g. schedule run logs, which have
// no replica concept). Those lines must omit the "replica" key entirely.
func TestStreamWriter_NDJSONNilReplicaOmitted(t *testing.T) {
	var out bytes.Buffer
	w := newStreamWriter(&out, formatNDJSON, nil)
	w.WriteLine("no replica scope")
	var obj map[string]any
	if err := json.Unmarshal(bytes.TrimRight(out.Bytes(), "\n"), &obj); err != nil {
		t.Fatalf("not NDJSON: %q: %v", out.String(), err)
	}
	if _, has := obj["replica"]; has {
		t.Error("replica key must be omitted when the stream is not replica-scoped")
	}
}
