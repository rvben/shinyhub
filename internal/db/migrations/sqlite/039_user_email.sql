-- Email address for a user, sourced from the identity provider on SSO login
-- (OAuth/OIDC). Empty by default and for local username/password accounts.
-- Persisting it lets the reverse proxy forward X-Shinyhub-Email (and the
-- identity token's email claim) for native session users, matching what the
-- forward-auth path already does from an upstream header.
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
