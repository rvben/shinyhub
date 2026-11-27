package api

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// A failed write leaves the deployment pending, which refuses every later
// deploy of the app until a restart, so it must reach the log with the
// deployment it concerns.
func TestFailDeploymentLogsAFailedWrite(t *testing.T) {
	store := dbtest.New(t)
	s := &Server{store: store}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	s.failDeployment(42, "bundle content digest could not be established")

	out := buf.String()
	for _, want := range []string{"level=ERROR", "deployment_id=42", "bundle content digest could not be established", "err="} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}

// Every pending deployment the API ends as failed goes through failDeployment
// or checks the error itself; a discarded write hides a deployment stuck
// pending.
func TestNoDeploymentFailureWriteIsDiscarded(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checkedWrite := regexp.MustCompile(`if \w+ := s\.store\.FailDeployment`)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for n, line := range strings.Split(string(src), "\n") {
			// Both store methods count: FailDeployment is FailDeploymentWithReason
			// with no reason. A call is acceptable only where its error is bound
			// on the same line.
			if !strings.Contains(line, "s.store.FailDeployment") {
				continue
			}
			if !checkedWrite.MatchString(line) {
				t.Errorf("%s:%d does not check the error of a deployment failure write; use s.failDeployment", f, n+1)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no source files were scanned")
	}
}
