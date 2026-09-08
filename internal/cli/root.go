package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rvben/shinyhub/internal/auth"
)

// version is set by the parent binary (cmd/shinyhub) via SetVersion,
// which plumbs it in from `-ldflags "-X main.version=vX.Y.Z"`.
var version = "dev"

// httpClient is the shared HTTP client for all CLI commands.
// A 30-second timeout prevents indefinite hangs. For SSE streaming
// connections and deploys, use streamClient.
var httpClient = &apiClient{&http.Client{Timeout: 30 * time.Second}}

// streamClient is httpClient without a deadline, for the long-lived requests:
// SSE log streams, and deploys that spend minutes building an environment.
// It goes through the same wrapper so a server that cannot be reached reads the
// same way whichever request found that out.
var streamClient = &apiClient{http.DefaultClient}

// SetVersion updates the version string reported by CLI subcommands.
// Called by the parent binary's init() so both the server (`shinyhub serve`)
// and the CLI subcommands report the same version.
func SetVersion(v string) {
	version = v
}

// silenceUsageOnError sets SilenceUsage on cmd and all its descendants so
// cobra does not print the full usage block when a RunE returns an error.
// Usage printing is only helpful for argument/flag syntax errors, not for
// runtime errors like HTTP 4xx/5xx responses.
func silenceUsageOnError(cmd *cobra.Command) {
	cmd.SilenceUsage = true
	for _, sub := range cmd.Commands() {
		silenceUsageOnError(sub)
	}
}

// configPathOverride is set by the --config persistent flag. Empty means
// "use the default path (or SHINYHUB_CREDENTIALS / SHINYHUB_CONFIG)".
var configPathOverride string

// hostFlagOverride is set by the --host persistent flag: a one-off "run this
// against that server" that does not change the saved current host. The value
// is a selector, so it accepts either a saved host's name or a full URL.
var hostFlagOverride string

// AddCommandsTo registers every CLI subcommand onto the supplied root command
// and attaches global persistent flags shared by all subcommands.
func AddCommandsTo(root *cobra.Command) {
	// Register our completion command explicitly. Cobra's generated command can
	// print scripts, but stops short of installing them; ShinyHub adds a safe,
	// idempotent install/uninstall flow while preserving raw generation.
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().StringVar(&configPathOverride, "config", "",
		"Path to client credentials file (overrides $SHINYHUB_CREDENTIALS, $SHINYHUB_CONFIG, and the default)")
	root.PersistentFlags().StringVar(&hostFlagOverride, "host", "",
		"Saved host name or server URL to target for this command (overrides the current host; does not change it)")
	root.PersistentFlags().StringVarP(&outputFlagValue, "output", "o", "",
		"Output format: table, json, or ndjson; continuous commands require ndjson (default: table on a terminal or in CI; otherwise json/ndjson when piped)")
	root.PersistentFlags().BoolVarP(&quietFlag, "quiet", "q", false,
		"Suppress non-essential output")
	root.PersistentFlags().BoolVar(&noColorFlag, "no-color", false,
		"Disable colored output (also honours $NO_COLOR; color is off by default when not writing to a terminal)")
	root.AddCommand(
		newCICmd(),
		newCompletionCmd(root),
		newConnectCmd(),
		newDoctorCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newWhoamiCmd(),
		newHostsCmd(),
		newUseCmd(),
		newDevCmd(),
		newPlanCmd(),
		newApplyCmd(),
		newDeployCmd(),
		newDraftsCmd(),
		newAppsCmd(),
		newTopCmd(),
		newTokensCmd(),
		newServiceAccountsCmd(),
		newEnvCmd(),
		newDataCmd(),
		newScheduleCmd(),
		newShareCmd(),
		newProjectsCmd(),
		newUsersCmd(),
		newFleetCmd(),
		newManifestCmd(),
		newSchemaCmd(),
		newRunCmd(),
	)
	_ = root.RegisterFlagCompletionFunc("host", completeSavedHosts)
	// Wrap flag-parse errors as KindValidation so the error envelope carries
	// kind=validation instead of the internal fallback. This applies to all
	// subcommands unless a subcommand sets its own FlagErrorFunc (e.g. fleet).
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return &ExitCodeError{Code: 1, Kind: KindValidation, Err: err}
	})
	silenceUsageOnError(root)
	enforceUnknownSubcommandErrors(root)
}

// enforceUnknownSubcommandErrors gives every command group in the tree (a
// command with subcommands of its own but no action of its own - "apps",
// "env", "fleet", and every other group registered above) the same "unknown
// command" treatment cobra reserves for the ROOT command only. cobra's
// default subcommand-arg validator (legacyArgs) checks !cmd.HasParent()
// before producing an error, so a bare group node with no RunE silently
// accepted a mistyped subcommand (e.g. "apps frobnicate"), printed its own
// help, and exited 0 - the success exit code, for what is a typo. Root
// itself is skipped: its own legacyArgs handling already produces this error
// and is left untouched.
func enforceUnknownSubcommandErrors(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		if sub.HasSubCommands() && !sub.Runnable() {
			requireKnownSubcommand(sub)
		}
		enforceUnknownSubcommandErrors(sub)
	}
}

// requireKnownSubcommand turns a bare command-group node into a runnable
// command whose Args validator rejects any positional argument that is not a
// registered subcommand, and whose RunE reproduces the original no-args
// behaviour (print help, exit 0). cobra only consults a command's Args
// validator once the command is Runnable - Command.execute short-circuits to
// printing help before ever reaching ValidateArgs otherwise - so both fields
// have to change together.
func requireKnownSubcommand(cmd *cobra.Command) {
	cmd.Args = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return nil
		}
		msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
		// SuggestionsFor (unlike cobra's own internal findSuggestions, which this
		// mirrors) never defaults SuggestionsMinimumDistance away from its Go
		// zero value, so an unset 0 would reject every Levenshtein-based match
		// outright and only prefix matches would ever surface.
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if suggestions := cmd.SuggestionsFor(args[0]); len(suggestions) > 0 {
			msg += "\n\nDid you mean this?\n"
			for _, s := range suggestions {
				msg += "\t" + s + "\n"
			}
		}
		return errors.New(msg)
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	}
}

type cliConfig struct {
	Host  string `json:"host"`
	Token string `json:"token"`
}

// configPath returns the effective client credentials path, honouring (in order):
//  1. the --config persistent flag,
//  2. SHINYHUB_CREDENTIALS (preferred; unambiguously refers to the CLIENT credentials
//     file, distinct from the server-side `serve --config` flag),
//  3. SHINYHUB_CONFIG (legacy alias, still fully supported for back-compat),
//  4. ~/.config/shinyhub/config.json (default).
func configPath() string {
	if configPathOverride != "" {
		return configPathOverride
	}
	if v := os.Getenv("SHINYHUB_CREDENTIALS"); v != "" {
		return v
	}
	if v := os.Getenv("SHINYHUB_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "shinyhub", "config.json")
}

// loadConfig returns the credentials for the server this command targets. The
// server is chosen first (--host, then SHINYHUB_HOST, then the saved current
// host) and the token is then read from that server's own entry, so a token is
// only ever sent to the server it was issued by. SHINYHUB_TOKEN still overrides
// the stored token, which is the CI path: host plus token from the environment
// needs no credentials file at all.
func loadConfig() (*cliConfig, error) {
	st, err := loadStore()
	if err != nil {
		return nil, err
	}
	return st.resolve(hostFlagOverride, os.Getenv("SHINYHUB_HOST"), os.Getenv("SHINYHUB_TOKEN"))
}

// authHeader returns the correct Authorization header value for the stored
// token. cfg.Token is one of two things: a JWT minted by POST /api/auth/login
// (the `shinyhub login --username/--password` flow), or an API key / pre-shared
// deploy token (the `--token` flow, SHINYHUB_TOKEN, SHINYHUB_DEPLOY_TOKEN).
//
// The server validates a JWT only under the Bearer scheme and an API key /
// deploy token only under the Token scheme, so the CLI must pick the scheme
// that matches the credential. The scheme is decided by detecting the JWT
// structurally rather than assuming "anything without an shk_ prefix is a
// JWT": an opaque SHINYHUB_DEPLOY_TOKEN (e.g. `openssl rand -hex 32`, a UUID,
// a secrets-manager value) carries no shk_ prefix yet is a Token-scheme
// credential, not a JWT. Treating it as Bearer is the defect this guards
// against — the server then runs JWT validation, never keyLookup, and 401s.
func authHeader(token string) string {
	if looksLikeJWT(token) {
		return "Bearer " + token
	}
	return "Token " + token
}

// looksLikeJWT defers to the server's own detector. The client and the server
// must agree on which credentials are JWTs: the client picks the Authorization
// scheme from this answer and the server picks its validation path from the
// same one, so two copies that drift by a single edge case produce a 401 that
// neither side can explain.
func looksLikeJWT(token string) bool {
	return auth.LooksLikeJWT(token)
}

// saveConfig stores one server's credentials and makes it the current host,
// leaving every other saved host intact. It is the single-host entry point onto
// the multi-host store; use saveNamedConfig when the caller also has an alias.
func saveConfig(cfg *cliConfig) error {
	return saveNamedConfig(cfg, "", "")
}

// saveNamedConfig is saveConfig with the optional alias and the username the
// credential belongs to, so `shinyhub hosts` can report both without a request.
func saveNamedConfig(cfg *cliConfig, name, user string) error {
	st, err := loadStore()
	if err != nil {
		return err
	}
	st.setCredential(normalizeHost(cfg.Host), name, cfg.Token, user)
	return saveStore(st)
}
