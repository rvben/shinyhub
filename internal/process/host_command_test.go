package process

import (
	"context"
	"io"
	"os/exec"
	"testing"
)

func TestHostIsolationRoutesProjectCommandsAndFailsClosed(t *testing.T) {
	defer SetHostCommandRunner(nil)
	called := false
	SetHostCommandRunner(func(ctx context.Context, dir string, argv, env []string, out io.Writer) error {
		called = true
		if dir != "/srv/app/v1" || argv[0] != "must-not-run-on-controller" {
			t.Fatalf("wrong command %q %v", dir, argv)
		}
		_, err := io.WriteString(out, "isolated")
		return err
	})
	cmd := exec.Command("must-not-run-on-controller", "build")
	cmd.Dir = "/srv/app/v1"
	out, err := HostCombinedOutput(context.Background(), cmd)
	if err != nil || !called || string(out) != "isolated" {
		t.Fatalf("project escaped isolation: %s %v", out, err)
	}
}
