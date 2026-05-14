package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

func Load(path string) (*Config, string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", err
	}
	var cfg Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, "", err
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, "", err
	}
	return &cfg, filepath.Dir(abs), nil
}

func applyDefaults(cfg *Config) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8080"
	}
	if cfg.DefaultPDFDir == "" {
		cfg.DefaultPDFDir = "Attachments/Boox"
	}
	if cfg.DefaultNoteDir == "" {
		cfg.DefaultNoteDir = "00 Inbox"
	}
}

func validate(cfg *Config) error {
	if cfg.VaultDir == "" {
		return fmt.Errorf("vault_dir is required")
	}
	for _, r := range cfg.Routes {
		if r.From == "" {
			return fmt.Errorf("route.from is required")
		}
	}
	return nil
}
