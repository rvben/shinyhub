package api

import "golang.org/x/crypto/bcrypt"

// testHashPassword hashes pw at bcrypt.MinCost for test fixtures; see the
// twin helper in hash_helper_ext_test.go (package api_test) for the full
// rationale. Duplicated because an external test package cannot reach an
// in-package test helper and vice versa.
func testHashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	return string(b), err
}
