package cli

import "testing"

// TestKindTable_CoversSpecKinds pins the exact kind set and exit codes from
// the design spec. The schema generator and the error renderer both read
// this table, so it is the single source of truth.
func TestKindTable_CoversSpecKinds(t *testing.T) {
	want := map[Kind]int{
		KindValidation:           1,
		KindNotFound:             1,
		KindConfirmationRequired: 1,
		KindInternal:             1,
		KindAuth:                 3,
		KindNetwork:              3,
		KindTimeout:              3,
		KindRateLimit:            3,
		KindServerError:          3,
		KindPartialConvergence:   4,
		KindConflict:             5,
		KindServerNotReady:       6,
		KindJobFailed:            0, // passthrough: no fixed exit code
	}
	if len(kindTable) != len(want) {
		t.Fatalf("kindTable has %d entries, want %d", len(kindTable), len(want))
	}
	for _, ki := range kindTable {
		code, ok := want[ki.Kind]
		if !ok {
			t.Errorf("unexpected kind %q", ki.Kind)
			continue
		}
		if ki.ExitCode != code {
			t.Errorf("kind %q exit code = %d, want %d", ki.Kind, ki.ExitCode, code)
		}
		if ki.Desc == "" {
			t.Errorf("kind %q has empty description", ki.Kind)
		}
	}
}

func TestKindTable_RetryableSet(t *testing.T) {
	retryable := map[Kind]bool{
		KindNetwork: true, KindTimeout: true, KindRateLimit: true,
		KindServerError: true, KindServerNotReady: true,
	}
	for _, ki := range kindTable {
		if ki.Retryable != retryable[ki.Kind] {
			t.Errorf("kind %q retryable = %v, want %v", ki.Kind, ki.Retryable, retryable[ki.Kind])
		}
	}
}
