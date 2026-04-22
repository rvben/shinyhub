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

// version is set at build time via -ldflags "-X github.com/rvben/shinyhub/cli.version=vX.Y.Z".
var version = "dev"

// httpClient is the shared HTTP client for all CLI commands.
// A 30-second timeout prevents indefinite hangs. For SSE streaming
// connections, use http.DefaultClient directly.
var httpClient = &http.Client{Timeout: 30 * time.Second}

var rootCmd = &cobra.Command{
	Use:     "shiny",
	Short:   "ShinyHub CLI — deploy and manage Shiny apps",
	Version: version,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(loginCmd, deployCmd, appsCmd, tokensCmd, envCmd, dataCmd, scheduleCmd, shareCmd)
}

type cliConfig struct {
	Host  string `json:"host"`
	Token string `json:"token"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "shinyhub", "config.json")
}

func loadConfig() (*cliConfig, error) {
	f, err := os.Open(configPath())
	if err != nil {
		return nil, fmt.Errorf("not logged in — run `shinyhub login` first")
	}
	defer f.Close()
	var cfg cliConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
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
