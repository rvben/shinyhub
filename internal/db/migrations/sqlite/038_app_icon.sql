-- Per-app icon shown on the Launchpad tile, the dashboard card, and the app
-- detail header. icon_mime is the stored image's MIME type ('' means no icon,
-- so the UI falls back to the generated monogram avatar); icon_data holds the
-- raw bytes. The bytes live in the database (not on local disk) so a multi-node
-- control plane serves a consistent icon without shared storage.
ALTER TABLE apps ADD COLUMN icon_mime TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN icon_data BLOB;
