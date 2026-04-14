package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database DatabaseConfig `yaml:"database"`
	Server   ServerConfig   `yaml:"server"`
	Auth     AuthConfig     `yaml:"auth"`
	Storage  StorageConfig  `yaml:"storage"`
}

type DatabaseConfig struct {
	Driver string `yaml:"driver"` // "sqlite" or "postgres"
	DSN    string `yaml:"dsn"`
}

type ServerConfig struct {
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	BaseURL string `yaml:"base_url"`
}

type AuthConfig struct {
	Secret string `yaml:"secret"`
}

type StorageConfig struct {
	AppsDir string `yaml:"apps_dir"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Database: DatabaseConfig{Driver: "sqlite", DSN: "./data/shinyhub.db"},
		Server:   ServerConfig{Host: "0.0.0.0", Port: 8080},
		Storage:  StorageConfig{AppsDir: "./data/apps"},
	}
	if path != "" {
		f, err := os.Open(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("open config: %w", err)
		}
		if err == nil {
			defer f.Close()
			if err := yaml.NewDecoder(f).Decode(cfg); err != nil {
				return nil, fmt.Errorf("parse config: %w", err)
			}
		}
	}
	applyEnv(cfg)
	if cfg.Auth.Secret == "" {
		return nil, fmt.Errorf("auth.secret must be set (SHINYHUB_AUTH_SECRET)")
	}
	return cfg, nil
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("SHINYHUB_AUTH_SECRET"); v != "" {
		cfg.Auth.Secret = v
	}
	if v := os.Getenv("SHINYHUB_DB_DSN"); v != "" {
		cfg.Database.DSN = v
	}
	if v := os.Getenv("SHINYHUB_APPS_DIR"); v != "" {
		cfg.Storage.AppsDir = v
	}
	if v := os.Getenv("SHINYHUB_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
}
