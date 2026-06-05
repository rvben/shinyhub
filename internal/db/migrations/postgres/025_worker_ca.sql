-- Singleton row holding the shared worker CA (cert public, key encrypted).
CREATE TABLE cp_worker_ca (
    role        TEXT PRIMARY KEY,
    cert_pem    TEXT NOT NULL,
    key_pem_enc BYTEA NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
