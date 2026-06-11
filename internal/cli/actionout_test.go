package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRenderAction_JSON(t *testing.T) {
	resetFormatState(t)
	var out bytes.Buffer
	err := renderActionTo(&out, formatJSON, "stopped", map[string]any{"slug": "demo"}, "Stopped demo")
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	if obj["status"] != "stopped" || obj["slug"] != "demo" {
		t.Errorf("envelope = %v", obj)
	}
}

func TestRenderAction_TableProse(t *testing.T) {
	resetFormatState(t)
	var out bytes.Buffer
	_ = renderActionTo(&out, formatTable, "stopped", map[string]any{"slug": "demo"}, "Stopped demo")
	if out.String() != "Stopped demo\n" {
		t.Errorf("table mode = %q", out.String())
	}
}

func TestRenderAction_QuietSuppressesProse(t *testing.T) {
	resetFormatState(t)
	quietFlag = true
	t.Cleanup(func() { quietFlag = false })
	var out bytes.Buffer
	_ = renderActionTo(&out, formatTable, "stopped", map[string]any{"slug": "demo"}, "Stopped demo")
	if out.Len() != 0 {
		t.Errorf("quiet table mode should print nothing, got %q", out.String())
	}
}

func TestRenderAction_NilFieldsOmitted(t *testing.T) {
	resetFormatState(t)
	var out bytes.Buffer
	_ = renderActionTo(&out, formatJSON, "deleted", nil, "deleted app")
	var obj map[string]any
	if err := json.Unmarshal(out.Bytes(), &obj); err != nil {
		t.Fatalf("not JSON: %q: %v", out.String(), err)
	}
	if obj["status"] != "deleted" {
		t.Errorf("status missing: %v", obj)
	}
}
