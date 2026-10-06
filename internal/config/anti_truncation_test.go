package config

import (
	"strings"
	"testing"
)

func TestAntiTruncationConfigLayouts(t *testing.T) {
	for _, raw := range []string{
		"streaming:\n  anti-truncation:\n    enabled: true\n    models: [gemini-*]\n    max-attempts: 4\n",
		"config-version: 8\nrequests:\n  streaming:\n    anti-truncation:\n      enabled: true\n      models: [gemini-*]\n      max-attempts: 4\n",
	} {
		if strings.HasPrefix(raw, "config-version") {
			if err := ValidateV8Config([]byte(raw)); err != nil {
				t.Fatal(err)
			}
		}
		cfg, err := ParseConfigBytes([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		got := cfg.Streaming.AntiTruncation
		if !got.Enabled || got.MaxAttempts != 4 || len(got.Models) != 1 || got.Models[0] != "gemini-*" {
			t.Fatalf("bad configuration: %+v", got)
		}
		clone := cfg.CloneForRuntime()
		clone.Streaming.AntiTruncation.Models[0] = "changed"
		if cfg.Streaming.AntiTruncation.Models[0] != "gemini-*" {
			t.Fatal("shared model selection across runtime snapshots")
		}
	}
}
