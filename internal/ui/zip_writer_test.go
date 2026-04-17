package ui_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"hash/crc32"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// TestNativeZipWriter_RoundTrip drives the browser-shared zip-writer.js through
// a Node harness and validates the output with Go's archive/zip. Catches
// regressions in the hand-rolled ZIP byte layout, CRC-32, UTF-8 name flag,
// and empty-file handling.
func TestNativeZipWriter_RoundTrip(t *testing.T) {
	requireNode18(t)

	type manifestEntry struct {
		Path       string `json:"path"`
		BodyBase64 string `json:"body_base64"`
	}

	cases := []struct {
		name string
		path string
		body []byte
	}{
		{"ascii-python", "app.py", []byte("from shiny import App, ui\napp = App(ui.page_fluid(ui.h1(\"hi\")), None)\n")},
		{"nested-r-script", "lib/server/main.R", []byte("shinyApp(ui, server)\n")},
		{"empty-file", "empty.txt", []byte{}},
		{"utf8-filename", "data/Témïgo-小鸡.txt", []byte("héllo\n")},
		{"binary", "assets/bin.dat", []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd, 0x00, 0x00}},
		{"highly-compressible", "log.txt", bytes.Repeat([]byte("aaaaaaaa"), 256)},
		{"requirements", "requirements.txt", []byte("shiny>=1.0\npandas\n")},
	}

	manifest := make([]manifestEntry, len(cases))
	for i, c := range cases {
		manifest[i] = manifestEntry{Path: c.path, BodyBase64: base64.StdEncoding.EncodeToString(c.body)}
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	cmd := exec.Command("node", "testdata/run-zip-writer.mjs")
	cmd.Stdin = bytes.NewReader(manifestJSON)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node harness failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(out) == 0 {
		t.Fatal("node harness produced empty output")
	}

	r, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("archive/zip rejected output: %v", err)
	}

	if got, want := len(r.File), len(cases); got != want {
		t.Fatalf("zip has %d entries, want %d", got, want)
	}

	byPath := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		byPath[f.Name] = f
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, ok := byPath[c.path]
			if !ok {
				t.Fatalf("missing entry %q", c.path)
			}
			if f.Method != zip.Deflate {
				t.Errorf("method = %d, want %d (DEFLATE)", f.Method, zip.Deflate)
			}
			if f.NonUTF8 {
				t.Errorf("NonUTF8 flag set; want UTF-8 name flag (bit 11) on every entry")
			}
			if got, want := f.UncompressedSize64, uint64(len(c.body)); got != want {
				t.Errorf("uncompressed size = %d, want %d", got, want)
			}
			wantCRC := crc32.ChecksumIEEE(c.body)
			if f.CRC32 != wantCRC {
				t.Errorf("crc32 = %08x, want %08x", f.CRC32, wantCRC)
			}

			rc, err := f.Open()
			if err != nil {
				t.Fatalf("open entry: %v", err)
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("read entry: %v", err)
			}
			if !bytes.Equal(got, c.body) {
				t.Errorf("content mismatch\n got: %q\nwant: %q", got, c.body)
			}
		})
	}
}

// requireNode18 skips the test when Node is missing or older than v18, which
// is when CompressionStream('deflate-raw') became available in a stable release.
func requireNode18(t *testing.T) {
	t.Helper()
	out, err := exec.Command("node", "--version").Output()
	if err != nil {
		t.Skip("node not on PATH; skipping JS zip-writer round-trip test")
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	major, _, _ := strings.Cut(v, ".")
	n, err := strconv.Atoi(major)
	if err != nil || n < 18 {
		t.Skipf("node %s too old (need >= 18 for CompressionStream); skipping", v)
	}
}
