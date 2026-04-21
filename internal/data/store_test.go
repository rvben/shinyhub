package data

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPut_Atomic(t *testing.T) {
	root := t.TempDir()
	dest := "out.bin"
	body := bytes.NewBufferString("hello world")
	info, err := Put(root, dest, body, int64(body.Len()))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if info.Size != 11 {
		t.Errorf("size = %d", info.Size)
	}
	got, err := os.ReadFile(filepath.Join(root, dest))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("content = %q", got)
	}
	matches, _ := filepath.Glob(filepath.Join(root, UploadTempDir, "*"))
	if len(matches) != 0 {
		t.Errorf("tempfiles leftover: %v", matches)
	}
}

func TestPut_OverwritesExisting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString("new!")
	if _, err := Put(root, "x.txt", body, 4); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "x.txt"))
	if string(got) != "new!" {
		t.Errorf("content = %q", got)
	}
}

func TestPut_CreatesParentDirs(t *testing.T) {
	root := t.TempDir()
	body := bytes.NewBufferString("nested")
	if _, err := Put(root, "a/b/c.txt", body, 6); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a", "b", "c.txt")); err != nil {
		t.Error(err)
	}
}

func TestPut_RejectsInvalidPath(t *testing.T) {
	root := t.TempDir()
	_, err := Put(root, "../escape", strings.NewReader("x"), 1)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestPut_ConcurrentLastWriterWins(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			_, _ = Put(root, "race.txt",
				strings.NewReader(strings.Repeat(string('A'+rune(i)), 10)), 10)
		}()
	}
	wg.Wait()
	got, err := os.ReadFile(filepath.Join(root, "race.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("expected 10-byte file (atomic rename), got %d", len(got))
	}
	matches, _ := filepath.Glob(filepath.Join(root, UploadTempDir, "*"))
	if len(matches) != 0 {
		t.Errorf("tempfiles leftover: %v", matches)
	}
}

func TestPut_ComputesSHA256(t *testing.T) {
	root := t.TempDir()
	info, err := Put(root, "x", io.NopCloser(strings.NewReader("abc")), 3)
	if err != nil {
		t.Fatal(err)
	}
	if info.SHA256 != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256 = %s", info.SHA256)
	}
}
