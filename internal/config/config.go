package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration values.
type Config struct {
	Listen         string   `yaml:"listen"`
	PasswordHash   string   `yaml:"password_hash,omitempty"`
	PasswordText   *string  `yaml:"password_text,omitempty"`
	DefaultCommand []string `yaml:"default_command"`
	DefaultWorkDir string   `yaml:"default_work_dir"`
	MaxSessions    int      `yaml:"max_sessions"`
	BufferSize     int      `yaml:"buffer_size"`
	LogLevel       string   `yaml:"log_level"`
}

// Default returns a Config with sensible defaults.
func Default() Config {
	emptyText := ""
	return Config{
		Listen:         "127.0.0.1:8443",
		PasswordHash:   "<argon2id>",
		PasswordText:   &emptyText,
		DefaultCommand: []string{"powershell.exe"},
		DefaultWorkDir: "",
		MaxSessions:    32,
		BufferSize:     1048576,
		LogLevel:       "debug",
	}
}

// Load reads and parses a YAML config file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Save writes a Config to a YAML file.
func Save(path string, cfg Config) error {
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// ExeDir returns the directory containing the running executable.
func ExeDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// Path returns the full path to a file in the executable directory.
func Path(filename string) (string, error) {
	dir, err := ExeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filename), nil
}
