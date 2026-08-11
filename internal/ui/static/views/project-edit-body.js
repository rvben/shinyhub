// buildProjectPatchBody builds the PATCH /api/projects/{slug} body from the
// edit modal's field values. name and icon_emoji are always sent; description
// is included only when descriptionKnown is truthy.
//
// The server's PATCH contract is declared-only: an absent key means "leave
// this field alone", while a present key - including an empty string - means
// "set it to this value" (internal/api/projects.go). The edit modal's group
// objects come from groupApps(), which never carries a description, so the
// Description field starts empty whenever the real project row has not been
// fetched yet. Sending description unconditionally would turn "open Edit,
// change nothing, press Save" into "clear the description". Gating the key
// on descriptionKnown is what keeps that no-op edit a true no-op, while still
// letting an operator who DID load the real value clear it on purpose by
// saving an empty Description field.
export function buildProjectPatchBody({ name, iconEmoji, description, descriptionKnown }) {
  const body = { name, icon_emoji: iconEmoji };
  if (descriptionKnown) {
    body.description = description;
  }
  return body;
}
