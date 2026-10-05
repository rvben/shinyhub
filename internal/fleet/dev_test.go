package fleet

import (
	"strings"
	"testing"
)

func TestManifestDevSettings(t *testing.T) {
	m, problems := ParseManifest([]byte(`fleet_id = "demo"
[dev]
seed = "missing"
env_allow = ["AWS_PROFILE", "AWS_CONFIG_FILE"]
env = { IS_LOCAL = "1" }
[[app]]
slug = "sales"
source = "./sales"
`), "fleet.toml")
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if m.Dev.Seed != "missing" || m.Dev.Env["IS_LOCAL"] != "1" || len(m.Dev.EnvAllow) != 2 {
		t.Fatalf("dev=%+v", m.Dev)
	}
	if m.Apps[0].Config.Name != nil {
		t.Fatal("local config contaminated deployment config")
	}
}

func TestManifestRejectsInvalidDevSettings(t *testing.T) {
	for _, table := range []string{
		`seed = "auto"`, `env_allow = ["AWS_*"]`, `env = { PORT = "9000" }`,
		`env_allow = ["SHINYHUB_RUN_MODE"]`, `env = { "bad-name" = "x" }`,
		`unknown_option = "x"`, `env = { IS_LOCAL = 1 }`,
	} {
		t.Run(table, func(t *testing.T) {
			_, problems := ParseManifest([]byte("fleet_id = \"demo\"\n[dev]\n"+table+"\n[[app]]\nslug = \"sales\"\nsource = \"./sales\"\n"), "fleet.toml")
			if len(problems) == 0 {
				t.Fatal("invalid dev config accepted")
			}
			if !strings.Contains(problems[0].Error(), "fleet.toml") {
				t.Fatal(problems)
			}
		})
	}
}
