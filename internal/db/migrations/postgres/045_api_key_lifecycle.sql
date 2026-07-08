-- See sqlite/045. timestamptz matches the baseline's created_at.
ALTER TABLE api_keys ADD COLUMN expires_at timestamptz;
ALTER TABLE api_keys ADD COLUMN last_used_at timestamptz;
