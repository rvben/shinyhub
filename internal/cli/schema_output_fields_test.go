package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// jsonTagSet returns the set of top-level JSON field names on a struct type.
func jsonTagSet(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		if name := strings.Split(tag, ",")[0]; name != "" {
			out[name] = true
		}
	}
	return out
}

// TestSchema_AppOutputFieldsBackedByStruct guards the app read-commands' schema
// against silent drift from the wire contract. The server marshals db.App
// directly for GET /api/apps (list items) and inside the GET /api/apps/{slug}
// envelope, so every OutputField for "apps list" / "apps show" that names an
// app-object field must be a real db.App JSON tag. If the server renames an app
// field, this fails - forcing the annotation to be corrected - instead of the
// CLI's declared OutputFields silently diverging (the class the CLI review
// flagged: OutputFields is declarative and never checked against real output).
//
// Fields the handlers add to the response envelope or compute at request time
// (not columns of db.App) are listed explicitly, so a genuinely new app-struct
// field cannot hide behind the allowlist.
func TestSchema_AppOutputFieldsBackedByStruct(t *testing.T) {
	appTags := jsonTagSet(reflect.TypeOf(db.App{}))

	handlerAdded := map[string]bool{
		// list envelope, synthesized by renderList
		"items": true, "total": true, "limit": true, "offset": true,
		// apps show: envelope + request-time-computed fields (see handleGetApp)
		"replicas_status":                    true,
		"effective_max_sessions_per_replica": true,
		"effective_autoscale_target":         true,
		"can_manage":                         true,
		"autoscale_status":                   true,
		"global_autoscale_enabled":           true,
		"runtime_mode":                       true,
		"resource_enforcement":               true,
		"worker_isolation":                   true,
		"worker_grouped_size":                true,
		"worker_max_workers":                 true,
		"worker_max_session_lifetime_secs":   true,
		"worker_pool":                        true,
	}

	for _, cmd := range []string{"apps list", "apps show"} {
		ann, ok := schemaAnnotations[cmd]
		if !ok {
			t.Fatalf("no schema annotation for %q", cmd)
		}
		fields := append(append([]fieldSpec{}, ann.OutputFields...), ann.EnvelopeFields...)
		for _, f := range fields {
			if appTags[f.Name] || handlerAdded[f.Name] {
				continue
			}
			t.Errorf("%q output field %q is neither a db.App JSON tag nor a known handler-added field; "+
				"the schema has drifted from the wire contract (a db.App rename, or an annotation/allowlist that needs updating)", cmd, f.Name)
		}
	}
}
