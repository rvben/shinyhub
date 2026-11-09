package main

import (
	"strings"
	"testing"
)

// The park-budget and park-TTL startup wiring lives in configureProxyAdmission.
// Pin it by source search so a future edit cannot silently drop the wiring
// while still compiling and passing every other test.
func TestMainWiresRenderParkBudget(t *testing.T) {
	s := proxyAdmissionSource(t)
	for _, needle := range []string{
		"SetRenderParkBudget(",
		"SetRenderParkTTL(",
		"cfg.RenderParkMaxPerApp()",
		"cfg.RenderParkMaxTotal()",
		"cfg.RenderParkTTL()",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("proxy_admission.go missing render-park wiring %q", needle)
		}
	}
}
