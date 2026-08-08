package proxy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/proxy"
)

// TestMetricsDocCoversEveryRejectReason guards docs/metrics.md against drift
// from the reject vocabulary. The reason label is what an operator sees in
// Prometheus and on X-Shinyhub-Reject, and the reasons differ in what they mean
// you should do about them: pool-saturated says add capacity, render-paced says
// adding capacity will not help. An undocumented reason is therefore not a
// missing sentence, it is a value an operator can only guess at, and guessing
// wrong here means scaling up a box that is out of CPU, not out of workers.
//
// This is a full-coverage check, not a spot check: adding a reason without
// documenting it fails the build.
func TestMetricsDocCoversEveryRejectReason(t *testing.T) {
	const docPath = "../../docs/metrics.md"
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	doc := string(raw)
	for _, reason := range []proxy.RejectReason{
		proxy.ReasonUnknownSlug,
		proxy.ReasonPoolSaturated,
		proxy.ReasonPoolDegraded,
		proxy.ReasonAppNotReady,
		proxy.ReasonMemoryPressure,
		proxy.ReasonRenderPaced,
		proxy.ReasonCPUSaturation,
		proxy.ReasonRenderDeferred,
	} {
		if !strings.Contains(doc, "`"+string(reason)+"`") {
			t.Errorf("docs/metrics.md does not document reject reason %q; an operator "+
				"seeing it in Prometheus has no way to know what to do about it", reason)
		}
	}
}
