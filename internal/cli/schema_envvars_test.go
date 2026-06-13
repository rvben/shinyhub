package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestSchemaIncludesEnvVars makes the CI-critical environment variables
// discoverable from `shinyhub schema`: SHINYHUB_HOST/SHINYHUB_TOKEN are the
// whole non-interactive auth story and were previously invisible to tooling.
func TestSchemaIncludesEnvVars(t *testing.T) {
	root := &cobra.Command{Use: "shinyhub", Short: "test root"}
	doc := generateSchema(root)
	names := map[string]string{}
	for _, e := range doc.EnvVars {
		names[e.Name] = e.Description
	}
	for _, want := range []string{"SHINYHUB_HOST", "SHINYHUB_TOKEN", "SHINYHUB_CONFIG"} {
		if _, ok := names[want]; !ok {
			t.Errorf("schema env_vars missing %q", want)
		}
	}
	if desc := names["SHINYHUB_TOKEN"]; desc == "" {
		t.Error("SHINYHUB_TOKEN should carry a description")
	}
}
