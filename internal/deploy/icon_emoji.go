package deploy

import "github.com/rvben/shinyhub/internal/iconspec"

// ValidateIconEmoji accepts a single emoji grapheme cluster. Callers treat ""
// as "clear the emoji" and must not pass it here.
//
// The rules live in internal/iconspec, a leaf package, so the fleet manifest
// can validate a project icon without importing the deploy package.
func ValidateIconEmoji(s string) error { return iconspec.Validate(s) }
