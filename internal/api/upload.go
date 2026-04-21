package api

import (
	"errors"
	"mime/multipart"
	"net/http"
)

const maxBundleUploadSize int64 = 128 << 20

// maxBundleMemoryBuffer is the in-memory threshold passed to ParseMultipartForm.
// Anything larger spills to disk under os.TempDir; the caller is responsible
// for removing those spill files via the returned cleanup function. Exposed as
// a var so tests can shrink it to exercise the spill path.
var maxBundleMemoryBuffer int64 = 32 << 20

var (
	errBundleTooLarge = errors.New("bundle too large")
	errBundleMissing  = errors.New("bundle file required")
	errBundleInvalid  = errors.New("invalid bundle upload")
)

// readBundleUpload parses the multipart "bundle" upload and returns a reader
// over its contents. The caller MUST defer the returned cleanup, which closes
// the file (when present) and removes any temp files that ParseMultipartForm
// spilled to disk. cleanup is always non-nil so deferring it is unconditional.
func readBundleUpload(w http.ResponseWriter, r *http.Request, maxSize int64) (multipart.File, func(), error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	cleanup := func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}

	if err := r.ParseMultipartForm(maxBundleMemoryBuffer); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, cleanup, errBundleTooLarge
		}
		return nil, cleanup, errBundleInvalid
	}

	file, _, err := r.FormFile("bundle")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return nil, cleanup, errBundleMissing
		}
		return nil, cleanup, errBundleInvalid
	}

	return file, func() {
		_ = file.Close()
		cleanup()
	}, nil
}
