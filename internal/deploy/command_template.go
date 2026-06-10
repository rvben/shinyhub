package deploy

import (
	"fmt"
	"regexp"
	"strings"
)

// Placeholder grammar, exactly: \{[a-z_]+\}. Anything else containing braces
// (${VAR}, {1..5}, {Key:) is inert by construction. There is no escaping
// mechanism: a literal lowercase {word} argument cannot be expressed, and
// validation says so rather than passing a typo like {prot} through silently.
var placeholderRe = regexp.MustCompile(`\{[a-z_]+\}`)

const (
	placeholderPort    = "{port}"
	placeholderHost    = "{host}"
	placeholderDataDir = "{data_dir}"
)

// dataDirRelative is what {data_dir} substitutes to: the persistent data
// directory relative to the app's working directory. Every runtime exposes
// it there - native symlinks <bundle>/data to the provisioned host volume,
// container runtimes mount AppDataPath at <workdir>/data.
const dataDirRelative = "data"

// validateCommandTemplate enforces the manifest command rules: non-empty
// array, non-empty elements, and only known placeholders. Runs at deploy
// time (LoadManifest) and again at boot (covers rollbacks to old bundles).
func validateCommandTemplate(cmd []string) error {
	if len(cmd) == 0 {
		return fmt.Errorf("`command` must be a non-empty array")
	}
	for i, arg := range cmd {
		if arg == "" {
			return fmt.Errorf("`command` element %d is empty", i+1)
		}
		for _, tok := range placeholderRe.FindAllString(arg, -1) {
			switch tok {
			case placeholderPort, placeholderHost, placeholderDataDir:
			default:
				return fmt.Errorf("`command` element %d: unknown placeholder %s (valid: {port}, {host}, {data_dir}; no escaping mechanism exists for literal lowercase {word} tokens)", i+1, tok)
			}
		}
	}
	return nil
}

// substituteCommand renders a validated template for one replica. It always
// builds a fresh slice: the template is shared across replica goroutines and
// must never be mutated (each replica gets its own port).
func substituteCommand(tpl []string, port int, bindHost string) []string {
	out := make([]string, len(tpl))
	r := strings.NewReplacer(
		placeholderPort, fmt.Sprintf("%d", port),
		placeholderHost, bindHost,
		placeholderDataDir, dataDirRelative,
	)
	for i, arg := range tpl {
		out[i] = r.Replace(arg)
	}
	return out
}
