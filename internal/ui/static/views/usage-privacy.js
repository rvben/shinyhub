// Translates the fixed set of server-authored usage_identity_mode validation
// messages (internal/api/apps.go's PATCH /api/apps/{slug} handler) into copy
// that names the on-screen label ("Usage identity granularity") rather than
// the wire field name, matching the plain-language wording used elsewhere in
// the same settings section. A message outside this known set is returned
// unchanged: guessing at an unrecognized server message risks hiding real
// information behind a made-up rewrite.
const KNOWN_USAGE_PRIVACY_ERRORS = {
  'usage_identity_mode cannot collect more identity than the hub policy':
    'This app cannot collect more viewer identity than the hub policy allows.',
  'usage_identity_mode must be disabled, unattributed, pseudonymous, identified, or null':
    'Usage identity granularity must be one of the options offered.',
};

export function usagePrivacyErrorMessage(rawMessage) {
  return KNOWN_USAGE_PRIVACY_ERRORS[rawMessage] || rawMessage;
}
