package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// defaultServer is the anonymous loopback listener the Tuxosaurus systemd
// unit opens.
const defaultServer = "http://127.0.0.1:51433"

// Config is ~/.config/rexctl/config.json:
//
//	{"default": "mac", "servers": {"mac": "https://host.ts.net", "tux": "unix:///run/user/1000/rex.sock"}}
type Config struct {
	Default string            `json:"default"`
	Servers map[string]string `json:"servers"`
}

// configPath honors REXCTL_CONFIG, then XDG_CONFIG_HOME, then ~/.config.
func configPath(getenv func(string) string) string {
	if p := getenv("REXCTL_CONFIG"); p != "" {
		return p
	}
	dir := getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := getenv("HOME")
		if home == "" {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "rexctl", "config.json")
}

func loadConfig(path string) (Config, error) {
	var c Config
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// resolveServer picks the server URL: the --server flag, then REX_SERVER,
// then the config default, then the local Tuxosaurus listener. Any of the
// first three may name an alias from the config file.
func resolveServer(flagValue string, getenv func(string) string, cfg Config) string {
	choice := flagValue
	if choice == "" {
		choice = getenv("REX_SERVER")
	}
	if choice == "" {
		choice = cfg.Default
	}
	if choice == "" {
		return defaultServer
	}
	if url, ok := cfg.Servers[choice]; ok {
		return url
	}
	return choice
}
