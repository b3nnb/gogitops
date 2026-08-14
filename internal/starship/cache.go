// Package starship writes the prompt cache file for Starship integration.
package starship

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// PromptData is the cache file starship reads
type PromptData struct {
	PeersTotal      int      `json:"peers_total"`
	PeersHealthy    int      `json:"peers_healthy"`
	ServicesHealthy bool     `json:"services_healthy"`
	ServicesDown    []string `json:"services_down"`
	DiskWarn        bool     `json:"disk_warn"`
	DiskWarnMounts  []string `json:"disk_warn_mounts"`
	Version         string   `json:"version"`
	Labels          []string `json:"labels"`
	Hostname        string   `json:"hostname"`
	NebulaRunning   bool     `json:"nebula_running"`
}

// CachePath returns the OS-appropriate cache file location
func CachePath() string {
	home, _ := os.UserHomeDir()
	// Linux + macOS both use ~/.cache. Windows would use %LOCALAPPDATA% (future).
	return filepath.Join(home, ".cache", "gogitops", "prompt.json")
}

// WriteCache writes the prompt.json for starship to read
func WriteCache(data PromptData) error {
	path := CachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}
