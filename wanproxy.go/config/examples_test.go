package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExampleJSONConfigsLoad(t *testing.T) {
	entries, err := os.ReadDir("../examples")
	if err != nil {
		t.Skipf("examples directory not available: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join("../examples", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(data); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
	}
}
