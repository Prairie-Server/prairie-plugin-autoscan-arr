// Package config parses the autoscan-arr plugin's global configuration.
//
// The plugin stores NO credentials. The arr base URL and API key are delivered
// by the host per poll in PollChangesRequest.connection. Plugin config carries
// only path rewrites that map arr file paths to Silo-native library paths.
package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Rewrite is a single prefix rewrite: paths under From are rewritten to To.
type Rewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Config holds the parsed plugin configuration.
type Config struct {
	Rewrites []Rewrite
}

// Parse builds a Config from a flat string map of config keys. The only
// recognized key is "path_rewrites", whose value is a JSON array of
// {"from","to"} objects. An empty or absent value yields a Config with no
// rewrites (paths pass through unchanged).
func Parse(values map[string]string) (*Config, error) {
	cfg := &Config{}
	raw := strings.TrimSpace(values["path_rewrites"])
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg.Rewrites); err != nil {
		return nil, fmt.Errorf("config: parse path_rewrites: %w", err)
	}
	return cfg, nil
}
