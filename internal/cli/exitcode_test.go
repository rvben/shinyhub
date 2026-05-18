package cli

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeError_ExitCode(t *testing.T) {
	base := errors.New("boom")
	e := &ExitCodeError{Code: 3, Err: base}

	if got := e.Error(); got != "boom" {
		t.Fatalf("Error() = %q, want %q", got, "boom")
	}
	if !errors.Is(e, base) {
		t.Fatalf("errors.Is(e, base) = false, want true (Unwrap must expose the cause)")
	}
	if got := exitCode(e); got != 3 {
		t.Fatalf("exitCode(e) = %d, want 3", got)
	}
	wrapped := fmt.Errorf("context: %w", e)
	if got := exitCode(wrapped); got != 3 {
		t.Fatalf("exitCode(wrapped) = %d, want 3 (must find ExitCodeError through wrapping)", got)
	}
	if got := exitCode(errors.New("plain")); got != 1 {
		t.Fatalf("exitCode(plain) = %d, want 1 (default for non-ExitCodeError)", got)
	}
	if got := exitCode(nil); got != 0 {
		t.Fatalf("exitCode(nil) = %d, want 0", got)
	}

	e2 := &ExitCodeError{Code: 2} // Err is nil
	if got := e2.Error(); got == "" {
		t.Fatalf("Error() with nil Err returned empty string")
	}
	if got := exitCode(e2); got != 2 {
		t.Fatalf("exitCode(nil-Err ExitCodeError) = %d, want 2", got)
	}
}
