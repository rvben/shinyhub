-- Binds each OAuth CSRF state nonce to the provider it was minted for, so a
-- state issued for one provider's login flow (e.g. GitHub) cannot be
-- consumed by a different provider's callback (e.g. Google). '' is the
-- default for rows already in flight at upgrade time (minted before this
-- column existed); no real provider name is ever empty, so those rows fail
-- to consume exactly once and the user simply retries the login.
ALTER TABLE oauth_states ADD COLUMN provider TEXT NOT NULL DEFAULT '';
