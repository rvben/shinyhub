-- Singleton row holding the shared worker CA. The cert is public; the private
-- key is stored encrypted (secrets.Encrypt with a CA-domain HKDF key).
CREATE TABLE IF NOT EXISTS cp_worker_ca (
    role        TEXT PRIMARY KEY,
    cert_pem    TEXT NOT NULL,
    key_pem_enc BLOB NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
