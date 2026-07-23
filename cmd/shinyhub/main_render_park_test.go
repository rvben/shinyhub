package main

import (
	"os"
	"strings"
	"testing"
)

// The park-budget and park-TTL startup wiring lives in main.go. Pin it by
// source search so a future edit cannot silently drop the wiring while still
// compiling and passing every other test.
func TestMainWiresRenderParkBudget(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	s := string(src)
	for _, needle := range []string{
		"SetRenderParkBudget(",
		"SetRenderParkTTL(",
		"cfg.RenderParkMaxPerApp()",
		"cfg.RenderParkMaxTotal()",
		"cfg.RenderParkTTL()",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("main.go missing render-park wiring %q", needle)
		}
	}
}
