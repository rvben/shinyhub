package fleet

import (
	"fmt"
	"regexp"
	"sort"
)

// DevSettings are local-only defaults. They never become deployed app settings.
type DevSettings struct {
	EnvAllow []string          `toml:"env_allow"`
	Env      map[string]string `toml:"env"`
	Seed     string            `toml:"seed"`
}

var devEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (d DevSettings) problems() []string {
	var problems []string
	if d.Seed != "" && d.Seed != "never" && d.Seed != "always" && d.Seed != "missing" {
		problems = append(problems, "dev.seed must be never, always, or missing")
	}
	keys := make([]string, 0, len(d.Env))
	for key := range d.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range append(keys, d.EnvAllow...) {
		if !devEnvName.MatchString(key) {
			problems = append(problems, fmt.Sprintf("dev environment variable %q is not a valid name", key))
			continue
		}
		switch key {
		case "PORT", "SHINYHUB_APP_DATA", "SHINYHUB_APP_SLUG", "SHINYHUB_RUN_MODE":
			problems = append(problems, fmt.Sprintf("dev environment variable %s is managed by ShinyHub", key))
		}
	}
	return problems
}
