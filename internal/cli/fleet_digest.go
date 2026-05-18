package cli

import (
	"archive/zip"
	"bytes"
	"fmt"

	"github.com/rvben/shinyhub/internal/bundle"
)

// digestLocalDir computes the content digest of a source directory using the
// EXACT path the server uses: the same bundler (zipDir) that `deploy`
// uploads, then bundle.DigestZipReader — the same function the server runs
// over the received zip. Reusing both halves guarantees client/server parity
// by construction (spec §4.2); we never re-walk or re-filter independently.
func digestLocalDir(dir string) (string, error) {
	buf, _, err := zipDir(dir)
	if err != nil {
		return "", fmt.Errorf("bundle %s: %w", dir, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		return "", fmt.Errorf("read bundle of %s: %w", dir, err)
	}
	digest, err := bundle.DigestZipReader(zr)
	if err != nil {
		return "", fmt.Errorf("digest %s: %w", dir, err)
	}
	return digest, nil
}
