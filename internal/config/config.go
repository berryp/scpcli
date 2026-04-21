package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Host      string
	UserID    string
	Email     string
	ProjectID string
	AccessKey string
	SecretKey string
}

type scpConfigFile struct {
	Host      string `json:"host"`
	UserID    string `json:"user-id"`
	Email     string `json:"email"`
	ProjectID string `json:"project-id"`
}

type scpCredentialsFile struct {
	AuthMethod string `json:"auth-method"`
	AccessKey  string `json:"access-key"`
	SecretKey  string `json:"secret-key"`
}

// Load reads ~/.scp/config.json and ~/.scp/credentials.json, then overlays
// SCP_* environment variables. Env vars take precedence over files.
func Load() (Config, error) {
	cfg, err := loadFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read ~/.scp files: %v\n", err)
	}

	if v := os.Getenv("SCP_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("SCP_USER_ID"); v != "" {
		cfg.UserID = v
	}
	if v := os.Getenv("SCP_EMAIL"); v != "" {
		cfg.Email = v
	}
	if v := os.Getenv("SCP_PROJECT_ID"); v != "" {
		cfg.ProjectID = v
	}
	if v := os.Getenv("SCP_ACCESS_KEY"); v != "" {
		cfg.AccessKey = v
	}
	if v := os.Getenv("SCP_SECRET_KEY"); v != "" {
		cfg.SecretKey = v
	}

	if cfg.Host == "" {
		cfg.Host = "https://openapi.samsungsdscloud.com"
	}

	return cfg, cfg.Validate()
}

func loadFiles() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve home dir: %w", err)
	}
	scpDir := filepath.Join(home, ".scp")

	cfgFile, err := readJSON[scpConfigFile](filepath.Join(scpDir, "config.json"))
	if err != nil {
		return Config{}, fmt.Errorf("config.json: %w", err)
	}

	credFile, err := readJSON[scpCredentialsFile](filepath.Join(scpDir, "credentials.json"))
	if err != nil {
		return Config{}, fmt.Errorf("credentials.json: %w", err)
	}

	if credFile.AuthMethod != "access-key" {
		return Config{}, fmt.Errorf("unsupported auth-method %q (only \"access-key\" is supported)", credFile.AuthMethod)
	}

	return Config{
		Host:      cfgFile.Host,
		UserID:    cfgFile.UserID,
		Email:     cfgFile.Email,
		ProjectID: cfgFile.ProjectID,
		AccessKey: credFile.AccessKey,
		SecretKey: credFile.SecretKey,
	}, nil
}

func (c Config) Validate() error {
	missing := []string{}
	if c.ProjectID == "" {
		missing = append(missing, "project-id (SCP_PROJECT_ID)")
	}
	if c.AccessKey == "" {
		missing = append(missing, "access-key (SCP_ACCESS_KEY)")
	}
	if c.SecretKey == "" {
		missing = append(missing, "secret-key (SCP_SECRET_KEY)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config fields: %v", missing)
	}
	return nil
}

func readJSON[T any](path string) (T, error) {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return zero, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}
