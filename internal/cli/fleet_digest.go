package cli

import "fmt"

// digestLocalDir computes the content digest of a source directory using the
// EXACT path the server uses: the same bundler (zipDir) that `deploy`
// uploads, then bundle.DigestZipReader - the same function the server runs
// over the received zip. Reusing both halves guarantees client/server parity
// by construction; we never re-walk or re-filter independently.
func digestLocalDir(dir string) (string, error) {
	preview, err := buildBundlePreview(dir)
	if err != nil {
		return "", fmt.Errorf("bundle %s: %w", dir, err)
	}
	return preview.Digest, nil
}
