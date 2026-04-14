package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "shiny",
	Short: "ShinyHost CLI — deploy and manage Shiny apps",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(loginCmd, deployCmd, appsCmd, tokensCmd)
}

type cliConfig struct {
	Host  string `json:"host"`
	Token string `json:"token"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "shiny", "config.json")
}

func loadConfig() (*cliConfig, error) {
	f, err := os.Open(configPath())
	if err != nil {
		return nil, fmt.Errorf("not logged in — run `shiny login` first")
	}
	defer f.Close()
	var cfg cliConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(cfg *cliConfig) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(cfg)
}
