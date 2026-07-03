-- See sqlite/043. Stored as INTEGER (not BOOLEAN) so the shared scanApp int
-- scan works across both backends.
ALTER TABLE apps ADD COLUMN ephemeral_data_ack INTEGER NOT NULL DEFAULT 0;
