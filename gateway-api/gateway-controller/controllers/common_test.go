package controllers

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestContainsString(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		element  string
		expected bool
	}{
		{"Element exists", []string{"a", "b", "c"}, "b", true},
		{"Element does not exist", []string{"a", "b", "c"}, "d", false},
		{"Empty slice", []string{}, "a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsString(tt.slice, tt.element)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRemoveString(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		element  string
		expected []string
	}{
		{"Remove existing element", []string{"a", "b", "c"}, "b", []string{"a", "c"}},
		{"Remove non-existing element", []string{"a", "b", "c"}, "d", []string{"a", "b", "c"}},
		{"Empty slice", []string{}, "a", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeString(tt.slice, tt.element)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestLoadPrivateKey(t *testing.T) {
	t.Run("Valid private key file", func(t *testing.T) {
		// Create a temporary private key file
		tempFile, err := os.CreateTemp("", "test_key")
		assert.NoError(t, err)
		defer os.Remove(tempFile.Name())

		_, err = tempFile.WriteString("test-private-key")
		assert.NoError(t, err)
		tempFile.Close()

		key, err := loadPrivateKey(tempFile.Name())
		assert.NoError(t, err)
		assert.Equal(t, "test-private-key", string(key))
	})

	t.Run("Invalid private key file", func(t *testing.T) {
		_, err := loadPrivateKey("non-existent-file")
		assert.Error(t, err)
	})
}

func TestGetEnvOrDefault(t *testing.T) {
	t.Run("Environment variable exists, int", func(t *testing.T) {
		os.Setenv("TEST_ENV_VAR", "123")
		defer os.Unsetenv("TEST_ENV_VAR")

		result := getEnvOrDefault("TEST_ENV_VAR", 0)
		assert.Equal(t, 123, result)
	})
	t.Run("Environment variable exists, string", func(t *testing.T) {
		os.Setenv("TEST_ENV_VAR", "a string")
		defer os.Unsetenv("TEST_ENV_VAR")

		result := getEnvOrDefault("TEST_ENV_VAR", "a default")
		assert.Equal(t, "a string", result)
	})
	t.Run("Environment variable exists, int, wrong type", func(t *testing.T) {
		os.Setenv("TEST_ENV_VAR", "123.123")
		defer os.Unsetenv("TEST_ENV_VAR")

		result := getEnvOrDefault("TEST_ENV_VAR", 42)
		assert.Equal(t, 42, result)
	})

	t.Run("Environment variable does not exist, int", func(t *testing.T) {
		result := getEnvOrDefault("NON_EXISTENT_ENV_VAR", 42)
		assert.Equal(t, 42, result)
	})
	t.Run("Environment variable does not exist, string", func(t *testing.T) {
		result := getEnvOrDefault("NON_EXISTENT_ENV_VAR", "a string")
		assert.Equal(t, "a string", result)
	})
}

func TestGetGwKey(t *testing.T) {
	result := getGwKey("namespace", "name")
	assert.Equal(t, "namespace/name", result)
}

func TestCreateListener(t *testing.T) {
	t.Run("Listener with hostname", func(t *testing.T) {
		hostname := gatewayv1.Hostname("example.com")
		listener := gatewayv1.Listener{
			Hostname: &hostname,
			Protocol: "HTTP",
			Port:     80,
		}

		result := createListener(listener)
		assert.Equal(t, "example.com", result.Hostname)
		assert.Equal(t, "HTTP", result.Protocol)
		assert.Equal(t, 80, result.Port)
	})

	t.Run("Listener without hostname", func(t *testing.T) {
		listener := gatewayv1.Listener{
			Protocol: "TCP",
			Port:     1234,
		}

		result := createListener(listener)
		assert.Equal(t, "0.0.0.0", result.Hostname)
		assert.Equal(t, "TCP", result.Protocol)
		assert.Equal(t, 1234, result.Port)
	})
}

func TestGetSvcHostname(t *testing.T) {
	result := getSvcHostname("service", "namespace")
	assert.Equal(t, "service.namespace.svc.cluster.local", result)
}
