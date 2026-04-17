package deploy_test

import (
	"archive/zip"
	"bytes"
	"os"
)

// createTestBundle creates a zip at path containing the given name→content pairs.
func createTestBundle(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	defer w.Close()
	for name, content := range files {
		fw, err := w.Create(name)
		if err != nil {
			return err
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			return err
		}
	}
	return nil
}

// createBombBundle writes a zip with a single entry of `size` zero-bytes.
// Zero bytes compress nearly perfectly, producing a tiny archive that expands
// to `size` when extracted — the classic zip-bomb shape.
func createBombBundle(path, name string, size int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	fw, err := w.Create(name)
	if err != nil {
		return err
	}
	if _, err := fw.Write(bytes.Repeat([]byte{0}, size)); err != nil {
		return err
	}
	return w.Close()
}
