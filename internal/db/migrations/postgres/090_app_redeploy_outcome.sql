-- Settings-redeploy sequence, claim and outcome. See sqlite/090.
ALTER TABLE apps ADD COLUMN redeploy_seq_launched BIGINT NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN last_redeploy_seq BIGINT NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN last_redeploy_outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN last_redeploy_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN last_redeploy_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN redeploy_claim_seq BIGINT NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN redeploy_claim_epoch BIGINT NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN redeploy_full_seq BIGINT NOT NULL DEFAULT 0;
