package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigurationFixtures(t *testing.T) {
	for _, kind := range []string{"valid", "invalid"} {
		files, err := filepath.Glob(filepath.Join("testdata", kind, "*.json"))
		if err != nil || len(files) == 0 {
			t.Fatalf("fixtures %s: %v", kind, err)
		}
		for _, path := range files {
			t.Run(path, func(t *testing.T) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				cfg, err := Parse(data)
				if cfg != nil {
					defer cfg.Close()
				}
				if (err == nil) != (kind == "valid") {
					t.Fatalf("expected %s: %v", kind, err)
				}
			})
		}
	}
}
