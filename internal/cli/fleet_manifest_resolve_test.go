package cli

import "testing"

// chooseFleetManifest decides which manifest a fleet command reads when -f may
// have been omitted. An explicit -f wins outright; otherwise fleet.toml is
// preferred and the legacy shinyhub-fleet.toml is a backward-compatible fallback
// that the operator is nudged to rename.
func TestChooseFleetManifest(t *testing.T) {
	const modern = defaultFleetManifest // "fleet.toml"
	const legacy = legacyFleetManifest  // "shinyhub-fleet.toml"

	cases := []struct {
		name       string
		explicit   bool
		flagValue  string
		present    map[string]bool
		wantPath   string
		wantLegacy bool
	}{
		{
			name:      "explicit -f is honored verbatim, even when it names the legacy file",
			explicit:  true,
			flagValue: "envs/eu/shinyhub-fleet.toml",
			present:   map[string]bool{modern: true, legacy: true},
			wantPath:  "envs/eu/shinyhub-fleet.toml",
		},
		{
			name:     "omitted: fleet.toml present wins, no fallback even if legacy also present",
			present:  map[string]bool{modern: true, legacy: true},
			wantPath: modern,
		},
		{
			name:       "omitted: only legacy present falls back to it",
			present:    map[string]bool{legacy: true},
			wantPath:   legacy,
			wantLegacy: true,
		},
		{
			name:     "omitted: neither present returns fleet.toml so the not-found error names it",
			present:  map[string]bool{},
			wantPath: modern,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exists := func(p string) bool { return tc.present[p] }
			gotPath, gotLegacy := chooseFleetManifest(tc.explicit, tc.flagValue, exists)
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotLegacy != tc.wantLegacy {
				t.Errorf("usedLegacy = %v, want %v", gotLegacy, tc.wantLegacy)
			}
		})
	}
}
