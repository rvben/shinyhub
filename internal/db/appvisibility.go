package db

// appVisibleToUserWhere is the four-path "which apps may this user see"
// predicate: public, shared, owned, explicit member, or group grant. It is a
// single constant because two queries now depend on it (the apps list and the
// projects list) and a divergence between them is an access-control hole that
// no test would notice: the projects list would simply reveal the existence and
// display name of a project the user cannot see any app in.
//
// It takes THREE placeholders, all bound to the SAME user ID, in order. It
// qualifies its columns with the apps table name so it is valid inside a
// subquery that joins other tables.
const appVisibleToUserWhere = `apps.access = 'public'
	   OR apps.access = 'shared'
	   OR apps.owner_id = ?
	   OR EXISTS (
	       SELECT 1 FROM app_members
	       WHERE app_slug = apps.slug AND user_id = ?
	   )
	   OR EXISTS (
	       SELECT 1 FROM app_group_access aga
	       JOIN user_groups ug ON ug.group_name = aga.group_name
	       WHERE aga.app_slug = apps.slug AND ug.user_id = ?
	   )`
