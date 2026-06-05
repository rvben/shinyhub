-- Per-deployment content digest: a stable sha256 over the accepted bundle
-- entries (same bundle.Rules filter the extractor applies), recorded on the
-- pending deployment row and only authoritative once the row is promoted to
-- 'succeeded'. The fleet reconcile layer compares this against a digest the
-- CLI computes from local source to decide "did this app's source change?".
-- Nullable: legacy rows stay NULL and are treated as "digest unknown" (the
-- first fleet plan reports update(source); the first apply self-heals it).
ALTER TABLE deployments ADD COLUMN content_digest TEXT;
