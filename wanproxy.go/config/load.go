package config

import (
	"fmt"
	"os"
)

// Load reads a JSON configuration. Paths inside it are relative to the
// process working directory, or may be absolute.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not open configuration: %w", err)
	}
	return Parse(data)
}
