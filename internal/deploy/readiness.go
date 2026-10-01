package deploy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// ProbeReadiness checks an existing endpoint once using the bundle's readiness
// contract. The caller bounds the context; redirects are accepted as responses
// rather than followed. This never starts or repairs a process.
func ProbeReadiness(ctx context.Context, endpointURL, bundleDir string, transport http.RoundTripper) error {
	m, err := LoadManifest(bundleDir)
	if err != nil {
		return fmt.Errorf("read readiness manifest: %w", err)
	}
	path, status := defaultReadinessPath(m), 0
	if m != nil {
		if m.App.ReadinessPath != "" {
			path = m.App.ReadinessPath
		}
		if m.App.ReadinessStatus != nil {
			status = *m.App.ReadinessStatus
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(endpointURL, "/")+"/"+strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if status > 0 && resp.StatusCode == status || status == 0 && resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return nil
	}
	return fmt.Errorf("readiness status %d", resp.StatusCode)
}
