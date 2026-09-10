package cli

import "fmt"

// digestLocalDir computes the content digest of a source directory using the
// EXACT path the server uses: the same bundler (zipDir) that `deploy`
// uploads, then bundle.DigestZipReader - the same function the server runs
// over the received zip. Reusing both halves guarantees client/server parity
// by construction; we never re-walk or re-filter independently.
func digestLocalDir(dir string) (string, error) {
	return digestBundleSpec(bundleBuildSpec{Dir: dir})
}

func digestBundleSpec(spec bundleBuildSpec) (string, error) {
	preview, err := previewBundleSpec(spec)
	if err != nil {
		return "", err
	}
	return preview.Digest, nil
}

// previewBundleSpec builds the upload exactly as apply would, so both the
// digest and the file list describe the bytes the server will receive.
func previewBundleSpec(spec bundleBuildSpec) (*bundlePreview, error) {
	preview, err := buildBundlePreviewFromSpec(spec)
	if err != nil {
		return nil, fmt.Errorf("bundle %s: %w", spec.Dir, err)
	}
	return preview, nil
}
