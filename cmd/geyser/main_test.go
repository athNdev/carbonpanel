package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// New configs must not expose the Bedrock listener on every interface by
// default. An operator who needs a wider bind opts in by setting
// BEDROCK_ADDRESS explicitly (the built-in Geyser module template does this
// because its UDP proxy needs a container-wide listener).
func TestGenerateConfigDefaultsBedrockAddressToLoopback(t *testing.T) {
	t.Setenv("BEDROCK_ADDRESS", "")

	cfg := generatedConfig(t)

	if !strings.Contains(cfg, "address: 127.0.0.1") {
		t.Fatalf("expected loopback bedrock.address by default, got:\n%s", cfg)
	}
	if strings.Contains(cfg, "address: 0.0.0.0") {
		t.Fatalf("default config must not bind all interfaces, got:\n%s", cfg)
	}
}

// An explicit BEDROCK_ADDRESS remains authoritative so existing module
// instances and direct-entrypoint deployments keep their documented behavior.
func TestGenerateConfigHonorsExplicitBedrockAddress(t *testing.T) {
	for _, address := range []string{"0.0.0.0", "::", "192.168.1.25", "127.0.0.1"} {
		t.Run(address, func(t *testing.T) {
			t.Setenv("BEDROCK_ADDRESS", address)

			cfg := generatedConfig(t)

			if !strings.Contains(cfg, "address: "+address) {
				t.Fatalf("explicit BEDROCK_ADDRESS %q was not preserved, got:\n%s", address, cfg)
			}
		})
	}
}

func TestResolveBedrockAddress(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "unset", value: "", want: "127.0.0.1"},
		{name: "loopback", value: "127.0.0.1", want: "127.0.0.1"},
		{name: "explicit wide bind", value: "0.0.0.0", want: "0.0.0.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BEDROCK_ADDRESS", tc.value)
			if got := resolveBedrockAddress(); got != tc.want {
				t.Fatalf("resolveBedrockAddress() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBedrockBindIsPublic(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "127.0.0.1", want: false},
		{address: "::1", want: false},
		{address: "192.168.1.25", want: false},
		{address: "0.0.0.0", want: true},
		{address: "::", want: true},
	}
	for _, tc := range tests {
		if got := bedrockBindIsPublic(tc.address); got != tc.want {
			t.Errorf("bedrockBindIsPublic(%q) = %v, want %v", tc.address, got, tc.want)
		}
	}
}

func generatedConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := generateConfig(path); err != nil {
		t.Fatalf("generateConfig: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	return string(data)
}
