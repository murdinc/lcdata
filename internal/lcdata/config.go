package lcdata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config is loaded from lcdata.json
type Config struct {
	Port              int    `json:"port"`
	JWTSecret         string `json:"jwt_secret"`
	RequireJWT        bool   `json:"require_jwt"`
	NodesPath         string `json:"nodes_path"`
	Env               string `json:"env"`
	LogLevel          string `json:"log_level"`
	MaxConcurrentRuns int    `json:"max_concurrent_runs"`
	RunTimeout        string `json:"run_timeout"`
	RunHistory        int    `json:"run_history"`

	RunTimeoutDuration time.Duration `json:"-"`
}

func DefaultConfig() *Config {
	return &Config{
		Port:              8080,
		JWTSecret:         "change-this-in-production",
		RequireJWT:        true,
		NodesPath:         "./nodes",
		Env:               "default",
		LogLevel:          "info",
		MaxConcurrentRuns: 10,
		RunTimeout:        "5m",
		RunHistory:        100,
	}
}

func LoadConfig() (*Config, error) {
	paths := []string{
		"./lcdata.json",
		filepath.Join(os.Getenv("HOME"), "lcdata.json"),
	}

	var configPath string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			configPath = p
			break
		}
	}

	cfg := DefaultConfig()

	if configPath == "" {
		if err := SaveConfig(cfg, "./lcdata.json"); err != nil {
			return nil, fmt.Errorf("failed to create default config: %w", err)
		}
		fmt.Println("Created default configuration: ./lcdata.json")
		return cfg, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config %s: %w", configPath, err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config %s: %w", configPath, err)
	}

	dur, err := time.ParseDuration(cfg.RunTimeout)
	if err != nil {
		return nil, fmt.Errorf("invalid run_timeout %q: %w", cfg.RunTimeout, err)
	}
	cfg.RunTimeoutDuration = dur

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func SaveConfig(cfg *Config, path string) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (c *Config) validate() error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid port: %d", c.Port)
	}
	if c.RequireJWT && c.JWTSecret == "" {
		return fmt.Errorf("jwt_secret required when require_jwt is true")
	}
	return nil
}
