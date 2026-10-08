package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestWhoamiEnvironmentMetadata(t *testing.T) {
	for _, label := range []string{"", "Acceptance"} {
		_, requests, setResp := setupCLITest(t)
		extra := ""
		if label != "" {
			extra = `,"environment_label":"Acceptance"`
		}
		setResp(200, `{"user":{"username":"viewer","role":"viewer"}`+extra+`}`)
		cmd := newWhoamiCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(out.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		got, present := body["environment_label"]
		if present != (label != "") || (present && got != label) {
			t.Fatal(body)
		}
		if len(*requests) != 1 {
			t.Fatal("whoami should not fetch extra metadata")
		}
	}
}
