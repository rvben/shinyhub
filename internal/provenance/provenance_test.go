package provenance

import "testing"

func TestMetadataValidate(t *testing.T) {
	good := Metadata{Provider: "gitlab", Source: &Link{Label: "Pipeline #42", URL: "https://gitlab.example/p/42"}, Revision: &Revision{SHA: "abc123", Ref: "main"}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid metadata: %v", err)
	}
	for _, tc := range []Metadata{
		{Provider: "GitLab"},
		{Source: &Link{URL: "http://example.test/pipeline"}},
		{Source: &Link{URL: "https://user:secret@example.test/pipeline"}},
		{Change: &Link{}},
	} {
		if err := tc.Validate(); err == nil {
			t.Fatalf("expected validation error for %#v", tc)
		}
	}
}

func TestValidRunID(t *testing.T) {
	if !ValidRunID("0123456789abcdef0123456789abcdef") {
		t.Fatal("valid run id rejected")
	}
	for _, bad := range []string{"", "run123", "0123456789ABCDEF0123456789ABCDEF", "0123"} {
		if ValidRunID(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
}
