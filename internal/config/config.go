// Package config persists the ps3 CLI's per-user state under ~/.ps3/.
//
// Stored:
//   server URL (e.g. http://localhost:8080)
//   JWT token (from login; refreshed on 401)
//   default bucket (used when a key doesn't include "bucket/")
//
// File permissions: 0600 — only the user can read; protects the JWT.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Server        string `json:"server"`        // e.g. http://localhost:8080
	Token         string `json:"token"`         // JWT
	Email         string `json:"email"`         // remembered for re-login UX
	DefaultBucket string `json:"defaultBucket"` // optional
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ps3", "config.json"), nil
}

func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ps3"), nil
}

// Load reads ~/.ps3/config.json. Returns a zero Config (no error) if the file
// doesn't exist — first-run is fine.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &c, nil
}

// Save writes the config back. Creates ~/.ps3/ if needed; file is 0600.
func Save(c *Config) error {
	d, err := dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	p, err := path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// MustServer panics if no server URL is configured — caller should print a
// friendlier hint to run `ps3 login` first.
func (c *Config) MustServer() string {
	if c.Server == "" {
		fmt.Fprintln(os.Stderr, "no server configured — run: ps3 login --server http://localhost:8080")
		os.Exit(2)
	}
	return c.Server
}
