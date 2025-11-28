package controllers

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	sshmgr "github.com/dguerri/ssh-gateway-api-controller/ssh"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Defaults for SSH connection, can be overridden by environment variables
const defaultKeyPath = "/ssh/id"
const defaultSSHServer = "localhost:22"
const defaultSSHUsername = "tunnel-user"
const defaultKeepAliveInterval = 10 * time.Second
const defaultConnectTimeout = 5 * time.Second

// Defaults for controller reconciliation periods
const (
	// defaultGatewayReconcilePeriod is how often the Gateway controller reconciles
	// to check SSH connection health and update status
	defaultGatewayReconcilePeriod = 30 * time.Second

	// defaultRouteReconcilePeriod is how often HTTPRoute/TCPRoute controllers reconcile
	// to retry failed route attachments and ensure routes are active
	defaultRouteReconcilePeriod = 10 * time.Second
)

var keyPath = getEnvOrDefault("SSH_PRIVATE_KEY_PATH", defaultKeyPath)
var keepAliveInterval = getEnvDurationOrDefault("KEEP_ALIVE_INTERVAL", defaultKeepAliveInterval)
var connectTimeout = getEnvDurationOrDefault("CONNECT_TIMEOUT", defaultConnectTimeout)
var sshServer = getEnvOrDefault("SSH_SERVER", defaultSSHServer)
var sshUsername = getEnvOrDefault("SSH_USERNAME", defaultSSHUsername)
var sshHostKey = getEnvOrDefault("SSH_HOST_KEY", "")

// Reconciliation period configuration
var gatewayReconcilePeriod = getEnvDurationOrDefault("GATEWAY_RECONCILE_PERIOD", defaultGatewayReconcilePeriod)
var routeReconcilePeriod = getEnvDurationOrDefault("ROUTE_RECONCILE_PERIOD", defaultRouteReconcilePeriod)

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

// getEnvOrDefault returns the environment variable value or a default string value.
func getEnvOrDefault(key string, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// getEnvDurationOrDefault returns the environment variable parsed as a duration or a default value.
func getEnvDurationOrDefault(key string, defaultValue time.Duration) time.Duration {
	value, exists := os.LookupEnv(key)
	if !exists {
		return defaultValue
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return defaultValue
	}
	return parsed
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
		RemoteAddrFunc:    getRemoteAddress,
	}

	return sshmgr.NewSSHTunnelManager(ctx, &sshConfig)
}

func getSvcHostname(svcName, svcNamespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", svcName, svcNamespace)
}

// getRemoteAddress extracts remote addresses from the input string using regex.
// Pico.sh TCP and HTTP(S) URIs are supported.
func getRemoteAddress(input string) ([]string, error) {
	var results []string

	patterns := map[string][]string{
		"tcp":   {`TCP\x1b\[0m:\s+([\w\.-]+:\d+)\r`},
		"http":  {`HTTP\x1b\[0m:\s+(http://[\w\.-]+)\r`},
		"https": {`HTTPS\x1b\[0m:\s+(https://[\w\.-]+)\r`},
	}

	for scheme, pats := range patterns {
		for _, pattern := range pats {
			re := regexp.MustCompile(pattern)
			matches := re.FindAllStringSubmatch(input, -1)
			for _, match := range matches {
				if len(match) > 1 {
					if scheme == "tcp" {
						results = append(results, "tcp://"+match[1])
					} else {
						results = append(results, match[1])
					}
				}
			}
		}
	}

	return results, nil
}
