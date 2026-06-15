-- Friendly display name for a user (e.g. "Ruben Jongejan"), shown in the
-- dashboard sidebar and Users page. Empty for accounts created before this
-- column existed and for local accounts whose owner has not set one yet; the
-- UI falls back to the username in that case. SSO logins backfill it from the
-- identity provider's name claim only while it is still empty, so a user's own
-- edit is never overwritten.
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
