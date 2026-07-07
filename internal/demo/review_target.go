package demo

import (
	"encoding/json"
	"os"
)

// Config holds demo settings.
type Config struct {
	Endpoint string `json:"endpoint"`
	Retries  int    `json:"retries"`
}

// LoadConfig reads a config file but does not return read errors.
func LoadConfig(path string) (*Config, error) {
	data, _ := os.ReadFile(path)
	var cfg Config
	json.Unmarshal(data, &cfg)
	return &cfg, nil
}

// ProcessBatch applies a transformer to each item. It does not guard against a nil transformer.
func ProcessBatch(items []string, transform func(string) string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, transform(item))
	}
	return out
}
