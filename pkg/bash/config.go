package bash

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LoadConfig loads configuration from YAML file
// Checks for project-local .bash-scanner.yaml first, then global ~/.bash-scanner.yaml
func LoadConfig(projectDir string) (*Config, error) {
	config := &Config{
		SafePaths:           []string{},
		WhitelistedCommands: []string{},
	}

	if projectDir != "" {
		localConfig := filepath.Join(projectDir, ".bash-scanner.yaml")
		if c, err := loadConfigFile(localConfig); err == nil {
			return c, nil
		}
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		globalConfig := filepath.Join(homeDir, ".bash-scanner.yaml")
		if c, err := loadConfigFile(globalConfig); err == nil {
			return c, nil
		}
	}

	return config, nil
}

func loadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}
