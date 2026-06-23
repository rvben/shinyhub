-- Optional one-line description shown on the viewer Launchpad (and the app
-- detail / Configuration tab). Empty by default; operators fill it in to give
-- end users context ("Q3 revenue by region") instead of a bare slug.
ALTER TABLE apps ADD COLUMN description TEXT NOT NULL DEFAULT '';
