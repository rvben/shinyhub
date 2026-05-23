package proxy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestScalingDocQuotesSaturation503Body guards docs/scaling.md against drift
// from the production 503 body. The doc shows users exactly what a shed
// request looks like; if the proxy text changes and the doc does not, this
// fails so the two stay in lockstep.
func TestScalingDocQuotesSaturation503Body(t *testing.T) {
	const docPath = "../../docs/scaling.md"
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	if !strings.Contains(string(raw), proxy.MsgPoolSaturated) {
		t.Errorf("docs/scaling.md must quote the production 503 body %q so the documented response matches what the proxy emits", proxy.MsgPoolSaturated)
	}
}
