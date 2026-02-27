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

	// Set test variables
	keyPath = "valid-key-path"
	sshServer = "test-server:22"
	sshUsername = "test-user"
	connectTimeout = 5 * time.Second
	keepAliveInterval = 10 * time.Second

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
	t.Run("Environment variable exists", func(t *testing.T) {
		os.Setenv("TEST_ENV_VAR", "test value")
		defer os.Unsetenv("TEST_ENV_VAR")

		result := getEnvOrDefault("TEST_ENV_VAR", "default value")
		assert.Equal(t, "test value", result)
	})

	t.Run("Environment variable does not exist", func(t *testing.T) {
		result := getEnvOrDefault("NON_EXISTENT_ENV_VAR", "default value")
		assert.Equal(t, "default value", result)
	})

	t.Run("Environment variable is empty string", func(t *testing.T) {
		os.Setenv("TEST_ENV_VAR", "")
		defer os.Unsetenv("TEST_ENV_VAR")

		result := getEnvOrDefault("TEST_ENV_VAR", "default value")
		assert.Equal(t, "", result)
	})
}

func TestGetEnvDurationOrDefault(t *testing.T) {
	t.Run("Environment variable exists with valid duration", func(t *testing.T) {
		os.Setenv("TEST_DURATION_VAR", "10s")
		defer os.Unsetenv("TEST_DURATION_VAR")

		result := getEnvDurationOrDefault("TEST_DURATION_VAR", 5*time.Second)
		assert.Equal(t, 10*time.Second, result)
	})

	t.Run("Environment variable exists with complex duration", func(t *testing.T) {
		os.Setenv("TEST_DURATION_VAR", "1h30m")
		defer os.Unsetenv("TEST_DURATION_VAR")

		result := getEnvDurationOrDefault("TEST_DURATION_VAR", 5*time.Second)
		assert.Equal(t, 90*time.Minute, result)
	})

	t.Run("Environment variable exists with invalid duration", func(t *testing.T) {
		os.Setenv("TEST_DURATION_VAR", "not a duration")
		defer os.Unsetenv("TEST_DURATION_VAR")

		result := getEnvDurationOrDefault("TEST_DURATION_VAR", 5*time.Second)
		assert.Equal(t, 5*time.Second, result)
	})

	t.Run("Environment variable does not exist", func(t *testing.T) {
		result := getEnvDurationOrDefault("NON_EXISTENT_VAR", 10*time.Second)
		assert.Equal(t, 10*time.Second, result)
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
		assert.Equal(t, "localhost", result.Hostname)
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

func TestGetRemoteAddress(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "Single TCP address",
			input:    "Starting SSH Forwarding service for \x1b[32mtcp:59123\x1b[0m. Forwarded connections can be accessed via the following methods:\r\n\x1b[44mTCP\x1b[0m: nue.tuns.sh:59123\r\n\r\n",
			expected: []string{"tcp://nue.tuns.sh:59123"},
		},
		{
			name:  "Multiple addresses of different types",
			input: "Starting SSH Forwarding service for \x1b[32mhttp:80\x1b[0m. Forwarded connections can be accessed via the following methods:\r\nService console can be accessed here: https://my-web-test.nue.tuns.sh/_sish/console?x-authorization=AKLjhgIOxLJTePkT1piV\r\n\x1b[44mHTTP\x1b[0m: http://my-web-test.nue.tuns.sh\r\n\x1b[44mHTTPS\x1b[0m: https://my-web-test.nue.tuns.sh\r\n\r\n",
			expected: []string{
				"http://my-web-test.nue.tuns.sh",
				"https://my-web-test.nue.tuns.sh",
			},
		},
		{
			name:     "No match",
			input:    "No matching address",
			expected: []string{},
		},
		{
			name:     "localhost.run HTTPS URI",
			input:    "7e0933afd6e392.lhr.life tunneled with tls termination, https://7e0933afd6e392.lhr.life\r\n",
			expected: []string{"https://7e0933afd6e392.lhr.life"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := getRemoteAddress(tt.input)
			assert.NoError(t, err)
			assert.ElementsMatch(t, tt.expected, result)
		})
	}
}

func TestGetHTTPRouteFinalizer(t *testing.T) {
	t.Run("default controller name", func(t *testing.T) {
		os.Unsetenv("GATEWAY_CONTROLLER_NAME")
		result := getHTTPRouteFinalizer()
		assert.Equal(t, "httproute.gateway.networking.k8s.io/finalizer-tunnels.ssh.gateway-api-controller", result)
	})

	t.Run("custom controller name", func(t *testing.T) {
		os.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/my-controller")
		defer os.Unsetenv("GATEWAY_CONTROLLER_NAME")
		result := getHTTPRouteFinalizer()
		assert.Equal(t, "httproute.gateway.networking.k8s.io/finalizer-example.com-my-controller", result)
	})
}

func TestGetTCPRouteFinalizer(t *testing.T) {
	t.Run("default controller name", func(t *testing.T) {
		os.Unsetenv("GATEWAY_CONTROLLER_NAME")
		result := getTCPRouteFinalizer()
		assert.Equal(t, "tcproute.gateway.networking.k8s.io/finalizer-tunnels.ssh.gateway-api-controller", result)
	})

	t.Run("custom controller name", func(t *testing.T) {
		os.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/my-controller")
		defer os.Unsetenv("GATEWAY_CONTROLLER_NAME")
		result := getTCPRouteFinalizer()
		assert.Equal(t, "tcproute.gateway.networking.k8s.io/finalizer-example.com-my-controller", result)
	})
}

func TestGetGatewayFinalizer(t *testing.T) {
	t.Run("default controller name", func(t *testing.T) {
		os.Unsetenv("GATEWAY_CONTROLLER_NAME")
		result := getGatewayFinalizer()
		assert.Equal(t, "gateway.networking.k8s.io/finalizer-tunnels.ssh.gateway-api-controller", result)
	})

	t.Run("custom controller name", func(t *testing.T) {
		os.Setenv("GATEWAY_CONTROLLER_NAME", "example.com/my-controller")
		defer os.Unsetenv("GATEWAY_CONTROLLER_NAME")
		result := getGatewayFinalizer()
		assert.Equal(t, "gateway.networking.k8s.io/finalizer-example.com-my-controller", result)
	})
}

func TestExtractHostnameFromURI(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		expected string
	}{
		{
			name:     "HTTP URI",
			uri:      "http://my-web-test.nue.tuns.sh",
			expected: "my-web-test.nue.tuns.sh",
		},
		{
			name:     "HTTPS URI",
			uri:      "https://user-dev.tuns.sh",
			expected: "user-dev.tuns.sh",
		},
		{
			name:     "HTTPS URI with port",
			uri:      "https://example.com:8443",
			expected: "example.com",
		},
		{
			name:     "TCP URI with host:port",
			uri:      "tcp://example.com:8080",
			expected: "example.com:8080",
		},
		{
			name:     "TCP URI with IP:port",
			uri:      "tcp://192.168.1.100:3000",
			expected: "192.168.1.100:3000",
		},
		{
			name:     "Plain hostname (no scheme)",
			uri:      "example.com",
			expected: "example.com",
		},
		{
			name:     "Hostname with port (no scheme)",
			uri:      "example.com:8080",
			expected: "example.com:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractHostnameFromURI(tt.uri)
			assert.Equal(t, tt.expected, result)
		})
	}
}
