package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamWriter_NDJSONWrapsLines(t *testing.T) {
	var out bytes.Buffer
	w := newStreamWriter(&out, formatNDJSON, 2)
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
	w := newStreamWriter(&out, formatTable, 0)
	w.WriteLine("raw line")
	if out.String() != "raw line\n" {
		t.Errorf("table mode must pass lines through: %q", out.String())
	}
}

func TestStreamWriter_NDJSONReplicaZeroOmitted(t *testing.T) {
	var out bytes.Buffer
	w := newStreamWriter(&out, formatNDJSON, 0)
	w.WriteLine("no replica")
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		// strip trailing newline
		if err2 := json.Unmarshal(bytes.TrimRight(out.Bytes(), "\n"), &obj); err2 != nil {
			t.Fatalf("not NDJSON: %q: %v", out.String(), err)
		}
	}
	if _, has := obj["replica"]; has {
		t.Error("replica key should be omitted when replica is 0")
	}
}
