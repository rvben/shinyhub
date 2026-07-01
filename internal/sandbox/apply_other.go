//go:build !linux

package sandbox

// Apply is a no-op on non-Linux platforms: the binaries build and run, but there
// is no isolation enforcement backend. Callers detect this via Supported and
// warn once at startup rather than per app.
func Apply(spec Spec) error { return nil }

// Supported reports whether this platform has an isolation enforcement backend.
func Supported() bool { return false }
