-- Emoji chosen as the app's icon, as a single UTF-8 grapheme cluster. '' means
-- "no emoji set", which is the correct fact for every pre-existing row: emoji
-- icons did not exist before this migration, so a defaulted '' is a confident
-- "none", not an unknown. The UI resolves the icon as emoji > uploaded image
-- (icon_mime/icon_data) > generated monogram, so '' here means "fall through to
-- the image or monogram".
ALTER TABLE apps ADD COLUMN icon_emoji TEXT NOT NULL DEFAULT '';
