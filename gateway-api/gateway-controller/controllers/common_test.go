package controllers

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestMain(m *testing.M) {
	// Perform setup tasks
	// Mock osReadFile for testing
	originalOsReadFile := osReadFile
	originalKeyPath := keyPath
	originalSSHServer := sshServer
	originalSSHUsername := sshUsername
	originalConnectTimeout := connectTimeout
	originalKeepAliveInterval := keepAliveInterval
	originalBackoffInterval := backoffInterval

	// Set test variables
	keyPath = "valid-key-path"
	sshServer = "test-server:22"
	sshUsername = "test-user"
	connectTimeout = 5 * time.Second
	keepAliveInterval = 10 * time.Second
	backoffInterval = 2 * time.Second

	osReadFile = func(path string) ([]byte, error) {
		if path == "valid-key-path" {
			return []byte(`-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAVwAAAAdzc2gtcn
NhAAAAAwEAAQAAAEEAqPfgaTEWEP3S9w0tgsicURfo+nLW09/0KfOPinhYZ4ouzU+3xC4p
SlEp8Ut9FgL0AgqNslNaK34Kq+NZjO9DAQAAATB+9/CSfvfwkgAAAAdzc2gtcnNhAAAAQQ
Co9+BpMRYQ/dL3DS2CyJxRF+j6ctbT3/Qp84+KeFhnii7NT7fELilKUSnxS30WAvQCCo2y
U1orfgqr41mM70MBAAAAAwEAAQAAAEAgkuLEHLaqkWhLgNKagSajeobLS3rPT0Agm0f7k5
5FXVt743hwNgkp98bMNrzy9AQ1mJGbQZGrpr4c8ZAx3aRNAAAAIBOs/5OiPgoTdSy7bcF9
IGpSE8ZgGKzgYQVZeN97YE00AAAAIQCjEr8yAZ54u6Lfzkontk5iS2OEsE0AHr18rBNkWx
Q2HQAAACEBCUEaRQnMnbp79mxDXDf6AU0cN/RPBjb9qSHDcWZHGzUAAAAXcGhwc2VjbGli
LWdlbmVyYXRlZC1rZXkBAgME
-----END OPENSSH PRIVATE KEY-----`), nil
		}
		return nil, errors.New("file not found")
	}

	// Run tests
	code := m.Run()

	// Perform teardown tasks
	// Restore the original osReadFile function
	osReadFile = originalOsReadFile
	keyPath = originalKeyPath
	sshServer = originalSSHServer
	sshUsername = originalSSHUsername
	connectTimeout = originalConnectTimeout
	keepAliveInterval = originalKeepAliveInterval
	backoffInterval = originalBackoffInterval

	// Exit with the test result code
	os.Exit(code)
}

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

func TestCreateSSHManager_Success(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager, err := createSSHManager(ctx)

	assert.NoError(t, err, "Expected no error when creating SSH manager")
	assert.NotNil(t, manager, "Expected SSH manager to be created")
}

func TestCreateSSHManager_InvalidKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	keyPath = "invalid-key-path"
	manager, err := createSSHManager(ctx)

	assert.Error(t, err, "Expected error when creating SSH manager with invalid key")
	assert.Nil(t, manager, "Expected SSH manager to be nil")
}
