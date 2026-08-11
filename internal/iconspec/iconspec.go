package iconspec

import "fmt"

// maxIconEmojiRunes bounds the value. The longest RGI sequence, a four-person
// family with skin tones, is 11 runes; 16 leaves headroom without admitting a
// paragraph.
const maxIconEmojiRunes = 16

// isEmojiBase reports whether r is in the emoji block allowlist. This is a
// fixed range table, not the Unicode Emoji property (the stdlib has none), so a
// newly-assigned emoji outside these blocks is rejected until the table is
// widened. A false reject is a minor annoyance on a cosmetic field.
func isEmojiBase(r rune) bool {
	switch {
	case r >= 0x1F600 && r <= 0x1F64F: // Emoticons
		return true
	case r >= 0x1F300 && r <= 0x1F5FF:
		// Misc Symbols & Pictographs, excluding the Fitzpatrick skin-tone
		// modifiers (0x1F3FB-0x1F3FF), which live inside this block but are
		// combining characters handled by isEmojiModifier, never bases.
		return r < 0x1F3FB || r > 0x1F3FF
	case r >= 0x1F680 && r <= 0x1F6FF: // Transport & Map
		return true
	case r >= 0x1F900 && r <= 0x1F9FF: // Supplemental Symbols & Pictographs
		return true
	case r >= 0x1FA70 && r <= 0x1FAFF: // Symbols & Pictographs Extended-A
		return true
	case r >= 0x2700 && r <= 0x27BF: // Dingbats
		return true
	case r >= 0x2600 && r <= 0x26FF: // Misc Symbols
		return true
	case r >= 0x2300 && r <= 0x23FF: // Misc Technical
		return true
	}
	return false
}

func isEmojiModifier(r rune) bool {
	return r == 0xFE0E || r == 0xFE0F || (r >= 0x1F3FB && r <= 0x1F3FF)
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func isKeycapBase(r rune) bool {
	return (r >= '0' && r <= '9') || r == '#' || r == '*'
}

// Validate accepts a single emoji grapheme cluster. Callers treat "" as
// "clear the emoji" and must not pass it here.
//
// It accepts ZWJ sequences, which is where the naive "first rune is a base and
// all the rest are modifiers" rule fails: in a profession sequence the rune
// after the ZWJ is itself a base.
func Validate(s string) error {
	if s == "" {
		return fmt.Errorf("icon must not be empty")
	}
	rs := []rune(s)
	if len(rs) > maxIconEmojiRunes {
		return fmt.Errorf("icon must be a single emoji (got %d characters, max %d)", len(rs), maxIconEmojiRunes)
	}

	// Flag form: exactly two regional indicators. No other position may hold one.
	if len(rs) == 2 && isRegionalIndicator(rs[0]) && isRegionalIndicator(rs[1]) {
		return nil
	}
	for _, r := range rs {
		if isRegionalIndicator(r) {
			return fmt.Errorf("icon must be a single emoji (a flag is exactly two regional indicators)")
		}
	}

	// Keycap form: base + U+FE0F + U+20E3, or base + U+20E3.
	if len(rs) == 3 && isKeycapBase(rs[0]) && rs[1] == 0xFE0F && rs[2] == 0x20E3 {
		return nil
	}
	if len(rs) == 2 && isKeycapBase(rs[0]) && rs[1] == 0x20E3 {
		return nil
	}

	// General form.
	if !isEmojiBase(rs[0]) {
		return fmt.Errorf("icon must be a single emoji")
	}
	afterZWJ := false
	for _, r := range rs[1:] {
		switch {
		case afterZWJ:
			// The rune immediately after a ZWJ must be a base.
			if !isEmojiBase(r) {
				return fmt.Errorf("icon must be a single emoji")
			}
			afterZWJ = false
		case isEmojiModifier(r):
			// Allowed after a base or another modifier.
		case r == 0x200D:
			afterZWJ = true
		default:
			// A base is allowed only immediately after a ZWJ, which is what
			// rejects two emoji written side by side.
			return fmt.Errorf("icon must be a single emoji")
		}
	}
	if afterZWJ {
		return fmt.Errorf("icon must be a single emoji (trailing joiner)")
	}
	return nil
}
