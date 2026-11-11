package cli

import "fmt"

// previewBundleSpec builds the upload exactly as apply would, so both the
// digest and the file list describe the bytes the server will receive.
func previewBundleSpec(spec bundleBuildSpec) (*bundlePreview, error) {
	preview, err := buildBundlePreviewFromSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("bundle %s: %w", spec.Dir, err)
	}
	return preview, nil
}
