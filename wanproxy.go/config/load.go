package config

import (
	"fmt"
	"os"
)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not open configuration: %w", err)
	}
	return Parse(data)
}
