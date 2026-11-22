//go:build darwin || windows

package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

//go:embed paseo.json
var initialConfig []byte

// Publish a complete initial config once; subsequent launches retain the user's UI settings.
func seedConfig(home, endpoint, adapter string) error {
	path := filepath.Join(home, "config.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var cfg struct {
		Version int            `json:"version"`
		Daemon  map[string]any `json:"daemon"`
		Agents  struct {
			Providers map[string]map[string]any `json:"providers"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(initialConfig, &cfg); err != nil {
		return err
	}
	cfg.Daemon["listen"] = endpoint
	cfg.Agents.Providers["cage-claude"]["command"] = []string{adapter}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, ".workstation-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	if err = errors.Join(writeErr, tmp.Close()); err != nil {
		return err
	}
	return publishConfig(tmp.Name(), path)
}
