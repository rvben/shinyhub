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
