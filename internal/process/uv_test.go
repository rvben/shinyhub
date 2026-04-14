package process_test

import (
	"os/exec"
	"testing"

	"github.com/rvben/shinyhub/internal/process"
)

func TestUVAvailable(t *testing.T) {
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv not in PATH — skipping integration test")
	}
	if err := process.CheckUV(); err != nil {
		t.Fatalf("uv check: %v", err)
	}
}
