package api_test

import "golang.org/x/crypto/bcrypt"

// testHashPassword hashes pw at bcrypt.MinCost for test fixtures. The
// production cost (12) is ~250ms per hash - several seconds under the race
// detector - and the api suite creates hundreds of users, which pushed the
// package's -race run past its 30m timeout. bcrypt hashes self-describe
// their cost, so auth.VerifyPassword accepts these fixtures unchanged.
func testHashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	return string(b), err
}
