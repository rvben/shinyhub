package api

import "testing"

// TestComputeRuntimes_PythonRequiresUv pins that Python availability tracks the
// actual launcher (uv), not python3. ShinyHub runs Python apps via `uv run` and
// syncs deps via `uv`, so a host with python3 but no uv cannot run a Python app
// and must not advertise python=true (that would make /api/server-info lie for
// the very preflight it exists to support).
func TestComputeRuntimes_PythonRequiresUv(t *testing.T) {
	rt := computeRuntimes(func(name string) bool { return name == "python3" })
	if rt["python"] {
		t.Error("python reported available with only python3 (no uv); uv is the launcher")
	}

	rt = computeRuntimes(func(name string) bool { return name == "uv" })
	if !rt["python"] {
		t.Error("python should be available when uv resolves")
	}

	rt = computeRuntimes(func(name string) bool { return name == "Rscript" })
	if !rt["r"] {
		t.Error("r should be available when Rscript resolves")
	}
}
