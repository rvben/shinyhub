package logstream

import (
	"reflect"
	"testing"
)

func TestRecordsFromBytesPreservesLinesAndAbsoluteOffsets(t *testing.T) {
	got := RecordsFromBytes([]byte("one\r\ntwo\npartial"), 10)
	want := []Record{
		{Line: "one", EndOffset: 15},
		{Line: "two", EndOffset: 19},
		{Line: "partial", EndOffset: 26},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecordsFromBytes = %#v, want %#v", got, want)
	}
	if got := RecordsFromBytes(nil, 50); got != nil {
		t.Fatalf("empty RecordsFromBytes = %#v, want nil", got)
	}
}
