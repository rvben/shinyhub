package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// ShimCommand is the hidden subcommand name the control plane re-execs itself as
// to sandbox a native app: `shinyhub <ShimCommand> -- <real command...>`.
const ShimCommand = "__sandbox"

// RunShim is the entry point for the re-exec shim subcommand. It reads the Spec
// from EnvVar, applies isolation to the current process, scrubs EnvVar from the
// environment, and execs the real command (everything after the first "--" in
// args). On success it never returns (the process image is replaced); it returns
// only on error.
func RunShim(args []string) error {
	cmd, err := splitCommand(args)
	if err != nil {
		return err
	}
	raw, ok := os.LookupEnv(EnvVar)
	if !ok {
		return fmt.Errorf("sandbox shim: %s not set", EnvVar)
	}
	spec, err := DecodeSpec(raw)
	if err != nil {
		return err
	}
	// Resolve the target before restricting, so PATH lookup is unaffected by the
	// sandbox and the resolved binary is what we exec.
	bin, err := exec.LookPath(cmd[0])
	if err != nil {
		return fmt.Errorf("sandbox shim: resolve %q: %w", cmd[0], err)
	}
	if err := Apply(spec); err != nil {
		return err
	}
	return syscall.Exec(bin, cmd, scrubEnv(os.Environ(), EnvVar))
}

// splitCommand returns the command following the first "--" separator in args.
func splitCommand(args []string) ([]string, error) {
	for i, a := range args {
		if a == "--" {
			cmd := args[i+1:]
			if len(cmd) == 0 {
				return nil, fmt.Errorf("sandbox shim: no command after '--'")
			}
			return cmd, nil
		}
	}
	return nil, fmt.Errorf("sandbox shim: missing '-- <command>' separator")
}

// scrubEnv returns environ with any entry for key removed, so the sandboxed app
// never sees its own isolation policy.
func scrubEnv(environ []string, key string) []string {
	prefix := key + "="
	out := environ[:0:0]
	for _, e := range environ {
		if strings.HasPrefix(e, prefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}
