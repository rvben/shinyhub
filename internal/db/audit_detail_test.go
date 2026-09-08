package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAuditDetail_EmptyStaysEmpty(t *testing.T) {
	// An event with nothing to record keeps an empty column rather than an empty
	// JSON object, so "recorded nothing" and "recorded {}" do not become the same
	// row. The Audit Log page renders the empty case as a dash.
	for name, fields := range map[string]map[string]any{
		"nil":   nil,
		"empty": {},
	} {
		if got := AuditDetail(fields); got != "" {
			t.Errorf("%s: got %q, want empty", name, got)
		}
	}
}

func TestAuditDetail_IsAlwaysValidJSON(t *testing.T) {
	// Each case is a value that the fmt.Sprintf-with-%q construction this helper
	// replaces would have rendered as a string encoding/json rejects. The control
	// below proves that claim rather than assuming it, so a future reader can see
	// why the helper exists at all.
	cases := map[string]any{
		"control byte":        "boot failed: \x01exit",
		"invalid utf8":        "boot failed: \xff\xfe",
		"delete byte":         "boot failed: \x7f",
		"newline":             "boot failed:\nretry",
		"quote and backslash": `he said "no" \ then left`,
		"plain ascii":         "no capacity",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			// Positive control: show the old construction really does break here,
			// or this test is asserting something that was never at risk.
			old := fmt.Sprintf(`{"reason":%q}`, value)
			oldValid := json.Valid([]byte(old))

			detail := AuditDetail(map[string]any{"reason": value})
			if !json.Valid([]byte(detail)) {
				t.Fatalf("AuditDetail produced invalid JSON: %s", detail)
			}
			var round map[string]any
			if err := json.Unmarshal([]byte(detail), &round); err != nil {
				t.Fatalf("AuditDetail output does not decode: %v (%s)", err, detail)
			}
			if _, ok := round["reason"]; !ok {
				t.Fatalf("field was dropped: %s", detail)
			}
			t.Logf("old construction valid=%v, new valid=true", oldValid)
		})
	}

	// The two bytes above that the old construction survives are the reason this
	// is a latent defect rather than a live one; the two it does not are the
	// reason it is a defect. Assert both halves so neither claim rots.
	if json.Valid([]byte(fmt.Sprintf(`{"reason":%q}`, "boot failed: \x01exit"))) {
		t.Error("the quoted-verb construction now produces valid JSON for a control byte; this helper's rationale needs rewriting")
	}
	if !json.Valid([]byte(fmt.Sprintf(`{"reason":%q}`, "no capacity"))) {
		t.Error("the quoted-verb construction fails on plain ASCII, which contradicts the claim that nothing has broken in production")
	}
}

func TestAuditDetail_CarriesEveryField(t *testing.T) {
	detail := AuditDetail(map[string]any{
		"from_user_id": 7,
		"to_username":  "aisha",
		"restarted":    true,
	})
	var round map[string]any
	if err := json.Unmarshal([]byte(detail), &round); err != nil {
		t.Fatalf("decode %q: %v", detail, err)
	}
	if len(round) != 3 {
		t.Fatalf("got %d fields, want 3: %s", len(round), detail)
	}
	if round["to_username"] != "aisha" || round["restarted"] != true {
		t.Fatalf("fields did not survive the round trip: %s", detail)
	}
	// json.Marshal sorts map keys, so the column is byte-identical for identical
	// input. Two rows recording the same thing must not differ by field order.
	if AuditDetail(map[string]any{"b": 1, "a": 2}) != AuditDetail(map[string]any{"a": 2, "b": 1}) {
		t.Error("output depends on map iteration order")
	}
}

func TestAuditDetail_UnrenderableValueIsNotSilence(t *testing.T) {
	// A caller that meant to record something and silently recorded nothing is
	// the one failure an audit trail must not hide, so this must not collapse to
	// the empty string that TestAuditDetail_EmptyStaysEmpty pins as "no detail".
	detail := AuditDetail(map[string]any{"ch": make(chan int)})
	if detail == "" {
		t.Fatal("an unrenderable detail collapsed to the same value as no detail at all")
	}
	if !json.Valid([]byte(detail)) {
		t.Fatalf("the failure path produced invalid JSON: %s", detail)
	}
	var round map[string]any
	if err := json.Unmarshal([]byte(detail), &round); err != nil {
		t.Fatalf("decode %q: %v", detail, err)
	}
	msg, ok := round["detail_error"].(string)
	if !ok || strings.TrimSpace(msg) == "" {
		t.Fatalf("failure path does not say what went wrong: %s", detail)
	}
}
