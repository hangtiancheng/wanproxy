package config

import (
	"fmt"
	"os"
	"strings"
)

// Load reads a configuration file, detecting the format: files whose
// first non-space, non-comment byte is '{' are parsed as JSON,
// everything else as the classic wanproxy.conf
// directive format of the C++ original.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not open file: %w", err)
	}
	return Parse(data)
}

// Parse detects the format of an in-memory configuration.
func Parse(data []byte) (*Config, error) {
	if looksLikeJSON(data) {
		return ParseJSON(data)
	}
	return NewClassicParser().Parse(string(data))
}

func looksLikeJSON(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return strings.HasPrefix(trimmed, "{")
	}
	return false
}
