package controllers

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	sshmgr "github.com/dguerri/ssh-gateway-api-controller/ssh"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Defaults for SSH connection, can be overridden by environment variables
const defaultKeyPath = "/ssh/id"
const defaultSSHServer = "localhost:22"
const defaultSSHUsername = "tunnel-user"
const defaultBackoffInterval = 5 * time.Second
const defaultKeepAliveInterval = 10 * time.Second
const defaultConnectTimeout = 5 * time.Second

var keyPath = getEnvOrDefault("SSH_PRIVATE_KEY_PATH", defaultKeyPath)
var backoffInterval = getEnvOrDefault("BACKOFF_INTERVAL", defaultBackoffInterval)
var keepAliveInterval = getEnvOrDefault("KEEP_ALIVE_INTERVAL", defaultKeepAliveInterval)
var connectTimeout = getEnvOrDefault("CONNECT_TIMEOUT", defaultConnectTimeout)
var sshServer = getEnvOrDefault("SSH_SERVER", defaultSSHServer)
var sshUsername = getEnvOrDefault("SSH_USERNAME", defaultSSHUsername)
var sshHostKey = getEnvOrDefault("SSH_HOST_KEY", "")

// Dependency injection for testing purposes
var osReadFile = os.ReadFile

// routeDetails holds the extracted details of a route.
type routeDetails struct {
	routeName      string
	routeNamespace string
	gwName         string
	gwNamespace    string
	listenerName   string
	backendHost    string
	backendPort    int
}

// containsString checks if a slice contains a string
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// removeString removes a string from a slice
func removeString(slice []string, s string) []string {
	result := make([]string, 0, len(slice))
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return result
}

func getEnvOrDefault[T any](key string, defaultValue T) T {
	value, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue
	}
	var parsedValue T
	switch any(defaultValue).(type) {
	case int:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return defaultValue
		}
		parsedValue = any(parsed).(T)
	case string:
		parsedValue = any(value).(T)
	default:
		return defaultValue
	}
	return parsedValue
}

func getGwKey(gwNamespace, gwName string) string {
	return fmt.Sprintf("%s/%s", gwNamespace, gwName)
}

func createListener(k8sListener gatewayv1.Listener) *Listener {
	remoteHostname := "0.0.0.0"
	if k8sListener.Hostname != nil {
		remoteHostname = string(*k8sListener.Hostname)
	}
	return &Listener{
		Hostname: remoteHostname,
		Protocol: string(k8sListener.Protocol),
		Port:     int(k8sListener.Port),
	}
}

func createSSHManager(ctx context.Context) (*sshmgr.SSHTunnelManager, error) {
	key, err := osReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("could not load private key: %w", err)
	}

	sshConfig := sshmgr.SSHConnectionConfig{
		ServerAddress:     sshServer,
		Username:          sshUsername,
		PrivateKey:        key,
		HostKey:           sshHostKey,
		ConnectTimeout:    connectTimeout,
		FwdReqTimeout:     2 * time.Second,
		KeepAliveInterval: keepAliveInterval,
		BackoffInterval:   backoffInterval,
	}

	return sshmgr.NewSSHTunnelManager(ctx, &sshConfig)
}

func getSvcHostname(svcName, svcNamespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", svcName, svcNamespace)
}
