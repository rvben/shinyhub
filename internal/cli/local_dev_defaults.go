package cli

import (
	"os"
	"sort"

	"github.com/rvben/shinyhub/internal/fleet"
	"github.com/spf13/cobra"
)

func configureSeedFlag(cmd *cobra.Command, value *string) {
	cmd.Flags().StringVar(value, "seed", "never", "Producer startup policy: never, missing, or always (bare --seed means always; never on reload)")
	cmd.Flags().Lookup("seed").NoOptDefVal = "always"
}

func withFleetDevDefaults(run localRunFlags, dev fleet.DevSettings, seedExplicit bool) localRunFlags {
	if !seedExplicit && dev.Seed != "" {
		run.seed = dev.Seed
	}
	defaults := map[string]string{}
	for _, key := range dev.EnvAllow {
		if value, ok := os.LookupEnv(key); ok {
			defaults[key] = value
		}
	}
	for key, value := range dev.Env {
		defaults[key] = value
	}
	keys := make([]string, 0, len(defaults))
	for key := range defaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	run.baseEnv = nil
	for _, key := range keys {
		run.baseEnv = append(run.baseEnv, key+"="+defaults[key])
	}
	return run
}
