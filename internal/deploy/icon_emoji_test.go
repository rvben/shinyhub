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
	for name, s := range accept {
		if err := ValidateIconEmoji(s); err != nil {
			t.Errorf("%s: ValidateIconEmoji(%q) = %v, want nil", name, s, err)
		}
	}

	reject := map[string]string{
		"letters":       "AB",
		"digits":        "12",
		"two bases":     "\U0001F4CA\U0001F4C8",
		"trailing ZWJ":  "\U0001F469‍",
		"four regional": "\U0001F1F3\U0001F1F1\U0001F1E9\U0001F1EA",
		"long string":   strings.Repeat("\U0001F4CA‍", 25),
		"empty":         "",
	}
	for name, s := range reject {
		if err := ValidateIconEmoji(s); err == nil {
			t.Errorf("%s: ValidateIconEmoji(%q) = nil, want error", name, s)
		}
	}
}
