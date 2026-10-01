package process

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"sync"
)

type HostCommandRunner func(context.Context, string, []string, []string, io.Writer) error

var hostCommands struct {
	sync.RWMutex
	runner HostCommandRunner
}

// SetHostCommandRunner is called once before serving. The isolated-native
// backend routes every app-directory host command through the broker.
func SetHostCommandRunner(r HostCommandRunner) {
	hostCommands.Lock()
	defer hostCommands.Unlock()
	hostCommands.runner = r
}
func HostUserIsolationEnabled() bool {
	hostCommands.RLock()
	defer hostCommands.RUnlock()
	return hostCommands.runner != nil
}
func RunHostCommand(ctx context.Context, cmd *exec.Cmd, out io.Writer) error {
	hostCommands.RLock()
	runner := hostCommands.runner
	hostCommands.RUnlock()
	if runner != nil && cmd.Dir != "" {
		return runner(ctx, cmd.Dir, cmd.Args, cmd.Env, out)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
func HostCombinedOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var out bytes.Buffer
	err := RunHostCommand(ctx, cmd, &out)
	return out.Bytes(), err
}
