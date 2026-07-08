-- Token lifecycle columns. expires_at NULL keeps the pre-existing
-- never-expires behavior; a non-NULL value makes the key unusable past that
-- instant (enforced in the auth lookup, not by a sweeper). last_used_at is a
-- coarse usage stamp (refreshed at most about once a minute per key on the
-- auth hot path) so operators can spot stale credentials worth revoking.
ALTER TABLE api_keys ADD COLUMN expires_at TIMESTAMP;
ALTER TABLE api_keys ADD COLUMN last_used_at TIMESTAMP;
