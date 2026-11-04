-- Successful request receipts live as long as the announcement history. A
-- claimed row is invisible until its response, announcement, and audit commit.
CREATE TABLE IF NOT EXISTS announcement_save_receipts (
    scope TEXT NOT NULL,
    request_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    response TEXT,
    PRIMARY KEY (scope, request_key)
);
