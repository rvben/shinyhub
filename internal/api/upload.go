package api

import (
	"errors"
	"mime/multipart"
	"net/http"
)

const (
	maxBundleUploadSize   int64 = 128 << 20
	maxBundleMemoryBuffer int64 = 32 << 20
)

var (
	errBundleTooLarge = errors.New("bundle too large")
	errBundleMissing  = errors.New("bundle file required")
	errBundleInvalid  = errors.New("invalid bundle upload")
)

func readBundleUpload(w http.ResponseWriter, r *http.Request, maxSize int64) (multipart.File, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	if err := r.ParseMultipartForm(maxBundleMemoryBuffer); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, errBundleTooLarge
		}
		return nil, errBundleInvalid
	}

	file, _, err := r.FormFile("bundle")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return nil, errBundleMissing
		}
		return nil, errBundleInvalid
	}

	return file, nil
}
