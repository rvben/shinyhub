package db

import "time"

// ConsumeTrustedAssertion atomically consumes an issuer/jti hash across all
// instances. Keep this separate from api_keys so revoking a credential cannot
// make the original assertion exchangeable again. Validation limits assertion
// age to ten minutes; one-hour retention leaves ample clock-skew margin.
func (s *Store) ConsumeTrustedAssertion(id string) (bool, error) {
	now := time.Now().UTC()
	if _, err := s.db.Exec(`DELETE FROM trusted_publishing_assertions WHERE expires_at < ?`, now); err != nil {
		return false, err
	}
	result, err := s.db.Exec(`INSERT INTO trusted_publishing_assertions(assertion_id, expires_at) VALUES (?, ?) ON CONFLICT (assertion_id) DO NOTHING`, id, now.Add(time.Hour))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
