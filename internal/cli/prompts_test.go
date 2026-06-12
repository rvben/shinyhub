package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestNonTTYRefusals verifies that each non-TTY refusal carries the right kind
// and a hint naming the bypass flag so the envelope's hint field is actionable.
func TestNonTTYRefusals(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantKind Kind
		wantHint string // substring the hint must contain
	}{
		{"login missing creds", loginMissingCredsError(), KindValidation, "--username"},
		{"apps set replicas", confirmationRequiredError("changing replicas restarts the app and drops sessions", "--yes"), KindConfirmationRequired, "--yes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, _ := classify(tc.err)
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			var he hintedError
			if !errors.As(tc.err, &he) || !strings.Contains(he.Hint(), tc.wantHint) {
				t.Errorf("hint must mention %q: %v", tc.wantHint, tc.err)
			}
		})
	}
}

// TestAppsSet_ReplicasNonTTYRefusesWithoutYes verifies that `apps set
// --replicas` on a non-TTY stdin without --yes returns KindConfirmationRequired
// and makes no network request. This is the spec behavior: automation that
// wants to scale via the CLI must pass --yes explicitly.
func TestAppsSet_ReplicasNonTTYRefusesWithoutYes(t *testing.T) {
	_, reqs, setResp := setupCLITest(t)
	setResp(200, `{}`)

	origIsTTY := isStdinTTY
	t.Cleanup(func() { isStdinTTY = origIsTTY })
	isStdinTTY = func() bool { return false }

	_, err := execCLI(t, "apps", "set", "demo", "--replicas", "3")
	if err == nil {
		t.Fatal("expected confirmation_required error on non-TTY without --yes, got nil")
	}
	kind, code := classify(err)
	if kind != KindConfirmationRequired {
		t.Errorf("classify(err).kind = %q, want %q", kind, KindConfirmationRequired)
	}
	if code != 1 {
		t.Errorf("classify(err).code = %d, want 1", code)
	}
	if len(*reqs) != 0 {
		t.Errorf("no network call must occur before the refusal, got %d requests", len(*reqs))
	}
	var he hintedError
	if !errors.As(err, &he) || !strings.Contains(he.Hint(), "--yes") {
		t.Errorf("hint must mention --yes: %v", err)
	}
}
