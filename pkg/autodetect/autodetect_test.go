package autodetect

import (
	"os"
	"testing"
)

func TestIsInfrastructureContainer(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"absia exact match", "absia", true},
		{"absia part of name", "opentelemetry-demo-absia-1", true},
		{"absia with caps", "ABSIA-backend", true},
		{"normal container", "frontend-proxy", false},
		{"normal container 2", "llm", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInfrastructureContainer(tt.input)
			if result != tt.expected {
				t.Errorf("isInfrastructureContainer(%q) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}

	// Test with ENV var
	os.Setenv("ABSIA_EXCLUDE_SERVICES", "redis, postgres")
	defer os.Unsetenv("ABSIA_EXCLUDE_SERVICES")

	envTests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"env match 1", "opentelemetry-demo-redis-1", true},
		{"env match 2", "postgres-db", true},
		{"env no match", "frontend-proxy", false},
	}

	for _, tt := range envTests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInfrastructureContainer(tt.input)
			if result != tt.expected {
				t.Errorf("isInfrastructureContainer(%q) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}
