package sandbox

import (
	"slices"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]struct {
		want Level
		err  bool
	}{
		"off":      {LevelOff, false},
		"standard": {LevelStandard, false},
		"":         {LevelOff, false}, // absent key => disabled
		"strict":   {"", true},        // reserved, not yet implemented
		"STANDARD": {"", true},        // case-sensitive
		"bogus":    {"", true},
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if want.err {
			if err == nil {
				t.Errorf("ParseLevel(%q): want error, got %q", in, got)
			}
			continue
		}
		if err != nil || got != want.want {
			t.Errorf("ParseLevel(%q) = %q, %v; want %q", in, got, err, want.want)
		}
	}
}

func TestLevelEnabled(t *testing.T) {
	if LevelOff.Enabled() || Level("").Enabled() {
		t.Error("off/empty must be disabled")
	}
	if !LevelStandard.Enabled() {
		t.Error("standard must be enabled")
	}
}

// ComputeSpec(standard) makes the app's own dirs writable plus the shared
// system scratch/device areas, and leaves reads open at "/".
func TestComputeSpec_Standard(t *testing.T) {
	s := ComputeSpec(LevelStandard, "/srv/app", "/data/app")
	if s.Level != LevelStandard {
		t.Fatalf("level = %q", s.Level)
	}
	wantWrite := []string{"/data/app", "/dev", "/srv/app", "/tmp"} // sorted
	if !slices.Equal(s.WritePaths, wantWrite) {
		t.Errorf("WritePaths = %v, want %v", s.WritePaths, wantWrite)
	}
	if !slices.Equal(s.ReadPaths, []string{"/"}) {
		t.Errorf("ReadPaths = %v, want [/] (reads open under standard)", s.ReadPaths)
	}
}

// /dev and /tmp must be writable under standard, else ordinary programs break
// (a denied /dev/null write fails shell redirects; this is proven on a live
// Landlock kernel).
func TestComputeSpec_Standard_IncludesDevAndTmp(t *testing.T) {
	s := ComputeSpec(LevelStandard, "/srv/app", "")
	if !slices.Contains(s.WritePaths, "/dev") || !slices.Contains(s.WritePaths, "/tmp") {
		t.Errorf("standard WritePaths must include /dev and /tmp, got %v", s.WritePaths)
	}
}

func TestComputeSpec_DropsEmptyDataDir(t *testing.T) {
	s := ComputeSpec(LevelStandard, "/srv/app", "")
	if slices.Contains(s.WritePaths, "") {
		t.Errorf("empty data dir must be dropped, got %v", s.WritePaths)
	}
}

// Callers can grant additional writable subtrees beyond the app/data dirs
// (the build phase needs the per-app managed-Python dir); every non-empty dir
// lands in WritePaths.
func TestComputeSpec_AdditionalWriteDirs(t *testing.T) {
	s := ComputeSpec(LevelStandard, "/srv/app", "/data/app", "/srv/uv-python")
	wantWrite := []string{"/data/app", "/dev", "/srv/app", "/srv/uv-python", "/tmp"} // sorted
	if !slices.Equal(s.WritePaths, wantWrite) {
		t.Errorf("WritePaths = %v, want %v", s.WritePaths, wantWrite)
	}
}

func TestComputeSpec_OffIsEmpty(t *testing.T) {
	s := ComputeSpec(LevelOff, "/srv/app", "/data/app")
	if s.Level.Enabled() || len(s.WritePaths) != 0 || len(s.ReadPaths) != 0 {
		t.Errorf("off must yield an empty policy, got %+v", s)
	}
}

func TestSpecEncodeRoundTrip(t *testing.T) {
	in := ComputeSpec(LevelStandard, "/srv/app", "/data/app")
	enc, err := in.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeSpec(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Level != in.Level || !slices.Equal(out.WritePaths, in.WritePaths) || !slices.Equal(out.ReadPaths, in.ReadPaths) {
		t.Errorf("round trip mismatch: %+v vs %+v", out, in)
	}
}
