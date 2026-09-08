package db

import (
	"encoding/json"
	"log/slog"
)

// AuditDetail renders the Detail column of an audit event as a JSON object, so
// that a reader — the Audit Log page, an operator's jq pipeline, a log shipper
// consuming the audit_event slog line — can parse the column instead of
// pattern-matching whichever prose shape a given action happens to use.
//
// Marshalling instead of formatting is the substance of it. Several call sites
// built this string with fmt.Sprintf and %q, which escapes non-printable bytes
// and invalid UTF-8 as \x.. and \u.. sequences that encoding/json rejects; a
// single such byte reaching one of those sites turns the whole row into a
// string nothing downstream can decode. Everything flowing into those sites is
// constrained to ASCII upstream today, so no operator has seen a broken row,
// but nothing was keeping it that way.
//
// An event with nothing worth recording passes nil and keeps an empty column.
// That is a different fact from an event whose detail failed to render, and the
// two stay distinguishable: a render failure produces a detail_error object
// rather than the empty string, because a caller that meant to say something
// and silently said nothing is the one case an audit trail must not hide.
func AuditDetail(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		// Reached only by a value no JSON encoder accepts (a channel, a func, a
		// cyclic structure), which is a programming error at the call site. Record
		// that the detail existed and could not be rendered; the action, resource
		// and principal on the same row still carry the security-relevant part.
		slog.Error("audit_detail_encode_failed", "err", err)
		fallback, _ := json.Marshal(map[string]any{"detail_error": err.Error()})
		return string(fallback)
	}
	return string(encoded)
}
