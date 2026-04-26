package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// version is set by the parent binary (cmd/shinyhub) via SetVersion,
// which plumbs it in from `-ldflags "-X main.version=vX.Y.Z"`.
var version = "dev"

// httpClient is the shared HTTP client for all CLI commands.
// A 30-second timeout prevents indefinite hangs. For SSE streaming
// connections, use http.DefaultClient directly.
var httpClient = &http.Client{Timeout: 30 * time.Second}

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
// "use the default path (or SHINYHUB_CONFIG)".
var configPathOverride string

// AddCommandsTo registers every CLI subcommand onto the supplied root command
// and attaches the global `--config` flag so any subcommand can be retargeted
// at a different credential file without re-running `login`.
func AddCommandsTo(root *cobra.Command) {
	root.PersistentFlags().StringVar(&configPathOverride, "config", "",
		"Path to credentials file (overrides $SHINYHUB_CONFIG and the default)")
	root.AddCommand(loginCmd, logoutCmd, deployCmd, appsCmd, tokensCmd, envCmd, dataCmd, scheduleCmd, shareCmd)
	silenceUsageOnError(root)
}

type cliConfig struct {
	Host  string `json:"host"`
	Token string `json:"token"`
}

// configPath returns the effective credentials path, honouring (in order):
//   1. the --config persistent flag,
//   2. the SHINYHUB_CONFIG environment variable,
//   3. ~/.config/shinyhub/config.json (default).
func configPath() string {
	if configPathOverride != "" {
		return configPathOverride
	}
	if v := os.Getenv("SHINYHUB_CONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "shinyhub", "config.json")
}

// loadConfig returns the effective credentials. The on-disk file is the
// baseline; SHINYHUB_HOST and SHINYHUB_TOKEN env vars override individual
// fields so a one-off command can target a different server (or use a CI
// token) without clobbering the saved config. If the env vars supply both
// fields the on-disk file is not required.
func loadConfig() (*cliConfig, error) {
	cfg := cliConfig{
		Host:  os.Getenv("SHINYHUB_HOST"),
		Token: os.Getenv("SHINYHUB_TOKEN"),
	}
	f, err := os.Open(configPath())
	if err != nil {
		if cfg.Host != "" && cfg.Token != "" {
			return &cfg, nil
		}
		return nil, fmt.Errorf("not logged in — run `shinyhub login` first")
	}
	defer f.Close()
	var fileCfg cliConfig
	if err := json.NewDecoder(f).Decode(&fileCfg); err != nil {
		return nil, err
	}
	if cfg.Host == "" {
		cfg.Host = fileCfg.Host
	}
	if cfg.Token == "" {
		cfg.Token = fileCfg.Token
	}
	if cfg.Host == "" || cfg.Token == "" {
		return nil, fmt.Errorf("incomplete credentials at %s — re-run `shinyhub login`", configPath())
	}
	return &cfg, nil
}

// authHeader returns the correct Authorization header value for the stored token.
// API keys (shk_ prefix) use the Token scheme; JWTs use Bearer.
func authHeader(token string) string {
	if strings.HasPrefix(token, "shk_") {
		return "Token " + token
	}
	return "Bearer " + token
}

func saveConfig(cfg *cliConfig) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(cfg)
}
