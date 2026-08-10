package deploy

import (
	"strings"
	"testing"
)

func TestValidateIconEmoji(t *testing.T) {
	accept := map[string]string{
		"base":                   "\U0001F4CA",
		"base ZWJ base":          "\U0001F469‍\U0001F52C",
		"base modifier ZWJ base": "\U0001F9D1\U0001F3FD‍\U0001F4BB",
		"four-person family":     "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466",
		"flag":                   "\U0001F1F3\U0001F1F1",
		"keycap":                 "1️⃣",
		"base modifier":          "⚙️",
	}

	// Pin the maxIconEmojiRunes boundary: exactly at the limit must accept, one
	// rune over must reject. Built from a valid ZWJ chain (8 bases joined by 7
	// ZWJs, 15 runes) plus a trailing skin-tone modifier (16th rune), with the
	// rune count asserted rather than assumed so a miscounted literal cannot
	// silently make the case vacuous.
	atLimit := strings.Repeat("\U0001F4CA‍", 7) + "\U0001F4CA" + "\U0001F3FB"
	if n := len([]rune(atLimit)); n != maxIconEmojiRunes {
		t.Fatalf("test setup: atLimit has %d runes, want %d", n, maxIconEmojiRunes)
	}
	accept["exactly max runes"] = atLimit

	for name, s := range accept {
		if err := ValidateIconEmoji(s); err != nil {
			t.Errorf("%s: ValidateIconEmoji(%q) = %v, want nil", name, s, err)
		}
	}

	reject := map[string]string{
		"letters":                     "AB",
		"digits":                      "12",
		"two bases":                   "\U0001F4CA\U0001F4C8",
		"trailing ZWJ":                "\U0001F469‍",
		"four regional":               "\U0001F1F3\U0001F1F1\U0001F1E9\U0001F1EA",
		"long string":                 strings.Repeat("\U0001F4CA‍", 25),
		"empty":                       "",
		"bare skin-tone modifier":     "\U0001F3FB",
		"base ZWJ skin-tone modifier": "\U0001F4CA‍\U0001F3FB",
	}

	// One rune past the boundary, otherwise a valid pattern, so length is the
	// only reason this is rejected.
	overLimit := atLimit + "\U0001F3FB"
	if n := len([]rune(overLimit)); n != maxIconEmojiRunes+1 {
		t.Fatalf("test setup: overLimit has %d runes, want %d", n, maxIconEmojiRunes+1)
	}
	reject["one over max runes"] = overLimit

	for name, s := range reject {
		if err := ValidateIconEmoji(s); err == nil {
			t.Errorf("%s: ValidateIconEmoji(%q) = nil, want error", name, s)
		}
	}
}
