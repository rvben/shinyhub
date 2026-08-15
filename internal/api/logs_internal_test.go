package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/logstream"
)

func TestWriteLogEventSignalsRetentionGapBeforeAdvancingCursor(t *testing.T) {
	rec := httptest.NewRecorder()
	writeLogEvent(rec, logstream.Record{Line: "earliest available", EndOffset: 42, GapBefore: true})
	body := rec.Body.String()
	want := "event: retention-gap\ndata: Output before this point is no longer retained\n\nid: 42\ndata: earliest available\n\n"
	if body != want {
		t.Fatalf("gap event body = %q, want %q", body, want)
	}
	gapEnd := strings.Index(body, "\n\n")
	if strings.Contains(body[:gapEnd], "id:") {
		t.Fatal("gap advisory advanced Last-Event-ID before the retained record was delivered")
	}
}

func TestWriteLogEventSignalsStreamHealthWithoutAdvancingCursor(t *testing.T) {
	for _, tt := range []struct {
		state logstream.StreamState
		want  string
	}{
		{logstream.StreamDegraded, "event: stream-degraded\ndata: Live output is temporarily delayed while retained log storage recovers\n\n"},
		{logstream.StreamRecovered, "event: stream-recovered\ndata: Live output delivery recovered\n\n"},
	} {
		rec := httptest.NewRecorder()
		writeLogEvent(rec, logstream.Record{StreamState: tt.state})
		if body := rec.Body.String(); body != tt.want {
			t.Errorf("%s event body = %q, want %q", tt.state, body, tt.want)
		} else if strings.Contains(body, "id:") {
			t.Errorf("%s event advanced Last-Event-ID", tt.state)
		}
	}
}
