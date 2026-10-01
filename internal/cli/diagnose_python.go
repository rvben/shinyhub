package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

type pythonDiagnosticFlags struct {
	python   string
	save     string
	duration time.Duration
	async    bool
}

type pythonDiagnosticReport struct {
	Status        string `json:"status"`
	PID           int    `json:"pid"`
	PythonVersion string `json:"python_version"`
	Path          string `json:"path,omitempty"`
	Stacks        string `json:"stacks,omitempty"`
}

func newDiagnoseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "diagnose", Short: "Inspect processes on this machine"}
	f := &pythonDiagnosticFlags{}
	python := &cobra.Command{
		Use:   "python <pid>",
		Short: "Inspect a local Python process with Python 3.15's sampling profiler",
		Long: `Inspect a Python process on this machine using Tachyon.

Run this command on the app host, with the Python interpreter PID (a uv
launcher PID is not a Python PID). Use --python to select an interpreter
matching the target's Python version and build. The interpreter must provide
profiling.sampling (Python 3.15 or newer).

By default, print a one-shot dump of all thread stacks. --async-aware includes
waiting asyncio tasks. With --save, collect a bounded profile and write a
private HTML flame graph. Existing files are never overwritten. Attaching
requires the operating system's process-inspection permissions; this command
does not change them or connect to a ShinyHub server.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPythonDiagnostic(cmd, args[0], f)
		},
	}
	python.Flags().StringVar(&f.python, "python", "python3.15", "Target-compatible Python interpreter executable")
	python.Flags().BoolVar(&f.async, "async-aware", false, "Include logical asyncio stacks and waiting tasks")
	python.Flags().StringVar(&f.save, "save", "", "Collect an HTML flame graph into a new private file")
	python.Flags().DurationVar(&f.duration, "duration", 30*time.Second, "Profile duration with --save (1s to 5m)")
	cmd.AddCommand(python)
	return cmd
}

func runPythonDiagnostic(cmd *cobra.Command, pidText string, f *pythonDiagnosticFlags) error {
	format, err := resolveFormat(false, false)
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("host") {
		return validationErr("diagnostics run on this machine; --host cannot select a remote process", "run the command on the app host")
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil || pid <= 0 {
		return validationErr("pid must be a positive integer", "use the Python interpreter PID on this machine")
	}
	if f.duration < time.Second || f.duration > 5*time.Minute || f.duration%time.Second != 0 {
		return validationErr("duration must be a whole number of seconds from 1s to 5m", "use --duration 30s")
	}
	if cmd.Flags().Changed("duration") && f.save == "" {
		return validationErr("--duration requires --save", "omit --duration for a one-shot stack dump")
	}
	if f.save != "" {
		if _, err := os.Lstat(f.save); err == nil {
			return fmt.Errorf("profile destination already exists: %s", f.save)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect profile destination: %w", err)
		}
	}
	path, err := exec.LookPath(f.python)
	if err != nil {
		return fmt.Errorf("Python interpreter unavailable: %w; select it with --python", err)
	}
	probeCtx, cancelProbe := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancelProbe()
	probe := exec.CommandContext(probeCtx, path, "-I", "-c", `import json,sys,importlib.util; print(json.dumps({"version":sys.version.split()[0],"supported":sys.version_info >= (3,15) and importlib.util.find_spec("profiling.sampling") is not None}))`)
	probe.WaitDelay = time.Second
	raw, err := probe.Output()
	if err != nil {
		return fmt.Errorf("cannot inspect Python interpreter: %w", err)
	}
	var info struct {
		Version   string `json:"version"`
		Supported bool   `json:"supported"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return fmt.Errorf("decode Python interpreter information: %w", err)
	}
	if !info.Supported {
		return fmt.Errorf("Python %s does not provide profiling.sampling; select Python 3.15 or newer with --python", info.Version)
	}
	args := []string{"-I", "-m", "profiling.sampling", "dump", "-a"}
	limit := 15 * time.Second
	var temporary string
	if f.save != "" {
		file, err := os.CreateTemp("", "shinyhub-profile-*.html")
		if err != nil {
			return err
		}
		temporary = file.Name()
		defer os.Remove(temporary)
		if err := file.Close(); err != nil {
			return err
		}
		args = []string{"-I", "-m", "profiling.sampling", "attach", "-a", "--flamegraph", "-d", strconv.Itoa(int(f.duration / time.Second)), "-o", temporary}
		limit = f.duration + 15*time.Second
	}
	if f.async {
		args = append(args, "--async-aware", "--async-mode", "all")
	}
	args = append(args, strconv.Itoa(pid))
	ctx, cancel := context.WithTimeout(cmd.Context(), limit)
	defer cancel()
	child := exec.CommandContext(ctx, path, args...)
	child.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	if err := child.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("Python diagnostic interrupted: %w", ctx.Err())
		}
		return fmt.Errorf("Python diagnostic failed: %w\n%s\nuse the Python PID and a matching interpreter, and check process-inspection permissions on the app host", err, stderr.String())
	}
	report := pythonDiagnosticReport{Status: "inspected", PID: pid, PythonVersion: info.Version, Stacks: stdout.String()}
	if f.save != "" {
		if err := copyPrivateProfile(temporary, f.save); err != nil {
			return fmt.Errorf("save profile: %w", err)
		}
		report.Status, report.Path, report.Stacks = "written", f.save, ""
	}
	if format == formatJSON {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
	}
	if report.Path != "" {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Profile saved to %s\n", report.Path)
	} else {
		_, err = fmt.Fprint(cmd.OutOrStdout(), report.Stacks)
	}
	return err
}

func copyPrivateProfile(source, destination string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	stat, err := in.Stat()
	if err != nil {
		return err
	}
	if stat.Size() == 0 {
		return fmt.Errorf("profiler produced an empty profile")
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.Remove(destination)
		}
	}()
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
