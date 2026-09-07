package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// HostConfig is one remote daemon host: an SSH destination as the user typed it.
type HostConfig struct {
	ID   string `json:"id"`
	Dest string `json:"dest"`
}

func hostsFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "tps", "hosts.json")
}

func loadHosts() []HostConfig {
	var saved struct {
		Hosts []HostConfig `json:"hosts"`
	}
	if data, err := os.ReadFile(hostsFile()); err == nil {
		_ = json.Unmarshal(data, &saved)
	}
	return saved.Hosts
}

func saveHosts(hosts []HostConfig) error {
	if hosts == nil {
		hosts = []HostConfig{}
	}
	data, _ := json.MarshalIndent(map[string]any{"hosts": hosts}, "", "\t")
	if err := os.MkdirAll(filepath.Dir(hostsFile()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(hostsFile(), append(data, '\n'), 0o644)
}
