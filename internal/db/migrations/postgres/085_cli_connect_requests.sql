-- Device-authorization style pending requests for `shinyhub connect`. The CLI
-- registers its token hash here before any browser page is opened; a browser
-- approval can only succeed by supplying the short user_code the CLI prints,
-- never by anything carried in a URL. One row per in-flight CLI, keyed by the
-- hash so a poll never needs the user_code.
CREATE TABLE IF NOT EXISTS cli_connect_requests (
    token_hash TEXT PRIMARY KEY,
    user_code  TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- UNIQUE, not a plain index: ConsumeCLIConnectRequest resolves a request by
-- user_code alone, so two pending rows sharing a code would let its DELETE
-- ... RETURNING match both, binding one CLI to the wrong browser session and
-- silently dropping the other's request. With 31^8 possible codes a
-- collision is unlikely, but the index is what turns "unlikely" into
-- "detected and retried" (see registerCLIConnectRequest in internal/api/auth.go)
-- rather than an unenforced assumption on a security path.
CREATE UNIQUE INDEX IF NOT EXISTS idx_cli_connect_requests_user_code_unique ON cli_connect_requests(user_code);
CREATE INDEX IF NOT EXISTS idx_cli_connect_requests_created_at ON cli_connect_requests(created_at);
