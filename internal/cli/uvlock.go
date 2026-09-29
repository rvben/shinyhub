package cli

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/rvben/shinyhub/internal/bundle"
	"github.com/rvben/shinyhub/internal/process"
)

// checkBundleLock runs the server's stale-lock check (process.CheckLockFiles)
// on the pyproject.toml and uv.lock in the archive a deploy would upload, so
// the verdict covers exactly the files the server receives: .shinyhubignore,
// the bundle filter rules and bundle inputs all apply. It returns nil when the
// archive does not ship both files, and an error wrapping
// process.ErrStaleLock, worded as the server words its refusal, for a lock the
// server would refuse.
func checkBundleLock(preview *bundlePreview) error {
	if preview == nil || preview.Buffer == nil {
		return errors.New("no bundle archive to check")
	}
	raw := preview.Buffer.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	var pyproject, lock []byte
	for _, f := range zr.File {
		var dst *[]byte
		switch f.Name {
		case "pyproject.toml":
			dst = &pyproject
		case "uv.lock":
			dst = &lock
		default:
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("read %s from bundle: %w", f.Name, err)
		}
		*dst, err = io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("read %s from bundle: %w", f.Name, err)
		}
	}
	if pyproject == nil || lock == nil {
		return nil
	}
	return process.CheckLockFiles(pyproject, lock)
}

// checkSourceLock builds the archive a deploy of dir (with bundle inputs)
// would upload and runs checkBundleLock on it.
func checkSourceLock(dir string, inputs []bundle.FileInputSnapshot) error {
	preview, err := buildBundlePreviewFromSpec(bundleBuildSpec{Dir: dir, Inputs: inputs})
	if err != nil {
		return fmt.Errorf("build the bundle to check its uv.lock: %w", err)
	}
	return checkBundleLock(preview)
}
