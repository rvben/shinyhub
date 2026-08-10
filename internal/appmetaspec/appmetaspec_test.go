package appmetaspec

import (
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain", in: "Quarterly Revenue", want: "Quarterly Revenue"},
		{name: "trims surrounding space", in: "  Sales  ", want: "Sales"},
		{name: "keeps interior space", in: "A  B", want: "A  B"},
		{name: "empty rejected", in: "", wantErr: true},
		{name: "whitespace only rejected", in: "   \t\n ", wantErr: true},
		{name: "at the limit", in: strings.Repeat("a", MaxNameRunes), want: strings.Repeat("a", MaxNameRunes)},
		{name: "over the limit", in: strings.Repeat("a", MaxNameRunes+1), wantErr: true},
		// The limit counts runes, not bytes: 128 three-byte characters encode to
		// 384 bytes, which a len()-based check would reject.
		{name: "multibyte at the limit", in: strings.Repeat("é", MaxNameRunes), want: strings.Repeat("é", MaxNameRunes)},
		{name: "multibyte over the limit", in: strings.Repeat("é", MaxNameRunes+1), wantErr: true},
		// Trimming happens before counting, so padding cannot push a legal name
		// over the limit.
		{name: "trimmed to the limit", in: " " + strings.Repeat("a", MaxNameRunes) + " ", want: strings.Repeat("a", MaxNameRunes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeName(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeName(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeName(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeDescription(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "plain", in: "Regional roll-up", want: "Regional roll-up"},
		{name: "trims surrounding space", in: "  hi  ", want: "hi"},
		// Unlike the name, "" is a legal value: it clears the description.
		{name: "empty is a value", in: "", want: ""},
		{name: "whitespace clears", in: "   ", want: ""},
		{name: "at the limit", in: strings.Repeat("a", MaxDescriptionRunes), want: strings.Repeat("a", MaxDescriptionRunes)},
		{name: "over the limit", in: strings.Repeat("a", MaxDescriptionRunes+1), wantErr: true},
		{name: "multibyte at the limit", in: strings.Repeat("é", MaxDescriptionRunes), want: strings.Repeat("é", MaxDescriptionRunes)},
		{name: "multibyte over the limit", in: strings.Repeat("é", MaxDescriptionRunes+1), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeDescription(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeDescription(%q) = %q, want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeDescription(%q): unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeDescription(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
