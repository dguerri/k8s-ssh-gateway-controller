package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sshmgr "github.com/dguerri/k8s-ssh-gateway-controller/ssh"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// Annotation key for enabling PROXY protocol on the SSH session.
// Valid values: "1" (PROXY protocol v1) or "2" (PROXY protocol v2).
const annotationProxyProtocol = "ssh-gateway.io/proxy-protocol"
const annotationSNIProxy = "ssh-gateway.io/sni-proxy"
const annotationListenerProxyProtocolPrefix = "ssh-gateway.io/listener-proxy-protocol."

// SessionKind is a type alias for sshmgr.SessionKind so there is a single enum.
// Controllers reference SessionPlain / SessionProxyProto / SessionSNIProxy directly.
type SessionKind = sshmgr.SessionKind

const (
	SessionPlain      = sshmgr.SessionPlain
	SessionProxyProto = sshmgr.SessionProxyProto
	SessionSNIProxy   = sshmgr.SessionSNIProxy
)

// Listener.Reason values for Rejected listeners. Surfaced as the
// Programmed=False condition Reason on the Gateway listener status (Task 7).
const (
	ReasonUnsupportedTLSMode          = "UnsupportedTLSMode"
	ReasonUnsupportedListenerProtocol = "UnsupportedListenerProtocol"
	ReasonSessionNotEnabled           = "SessionNotEnabled"
)

// ClassSessionConfig holds the typed session configuration derived from
// GatewayClass annotations.
type ClassSessionConfig struct {
	ProxyProtocolVersion int // 0 = disabled, 1 or 2
	SNIProxyEnabled      bool
}

// parseClassSessionConfig reads GatewayClass annotations and produces a typed
// session config. Invalid values log a warning (mirroring parseProxyProtocol)
// and disable the corresponding session.
func parseClassSessionConfig(annotations map[string]string) ClassSessionConfig {
	cfg := ClassSessionConfig{
		ProxyProtocolVersion: parseProxyProtocol(annotations),
	}
	if val, ok := annotations[annotationSNIProxy]; ok && val != "" {
		if strings.EqualFold(val, "true") {
			cfg.SNIProxyEnabled = true
		} else {
			slog.With("function", "parseClassSessionConfig").Warn(
				"invalid sni-proxy annotation value, ignoring",
				"value", val, "valid_values", `"true"`)
		}
	}
	return cfg
}

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

// controllerNameSuffix returns the controller name with '/' replaced by '-',
// so it can be safely embedded in a Kubernetes finalizer name (which only allows
// one '/' as a prefix separator: "<dns-prefix>/<name>").
func controllerNameSuffix() string {
	return strings.ReplaceAll(getGatewayControllerName(), "/", "-")
}

func getHTTPRouteFinalizer() string {
	return "httproute.gateway.networking.k8s.io/finalizer-" + controllerNameSuffix()
}

func getTCPRouteFinalizer() string {
	return "tcproute.gateway.networking.k8s.io/finalizer-" + controllerNameSuffix()
}

func getTLSRouteFinalizer() string {
	return "tlsroute.gateway.networking.k8s.io/finalizer-" + controllerNameSuffix()
}

func getGatewayFinalizer() string {
	return "gateway.networking.k8s.io/finalizer-" + controllerNameSuffix()
}

func getGwKey(gwNamespace, gwName string) string {
	return fmt.Sprintf("%s/%s", gwNamespace, gwName)
}

func createListener(k8sListener gatewayv1.Listener, gwAnnotations map[string]string) *Listener {
	remoteHostname := "localhost"
	if k8sListener.Hostname != nil {
		remoteHostname = string(*k8sListener.Hostname)
	}
	l := &Listener{
		Hostname: remoteHostname,
		Protocol: string(k8sListener.Protocol),
		Port:     int(k8sListener.Port),
	}
	deriveListenerSession(l, k8sListener, gwAnnotations)
	return l
}

// deriveListenerSession sets l.SessionKind (or l.Rejected + l.Reason) based on
// the listener protocol, its TLS config, and the Gateway-level annotations.
func deriveListenerSession(l *Listener, k8sListener gatewayv1.Listener, gwAnnotations map[string]string) {
	switch strings.ToUpper(l.Protocol) {
	case "HTTP":
		l.SessionKind = sshmgr.SessionPlain
	case "TCP":
		if parseListenerProxyProtocol(gwAnnotations, string(k8sListener.Name)) {
			l.SessionKind = sshmgr.SessionProxyProto
		} else {
			l.SessionKind = sshmgr.SessionPlain
		}
	case "TLS":
		if k8sListener.TLS != nil && k8sListener.TLS.Mode != nil && *k8sListener.TLS.Mode == gatewayv1.TLSModePassthrough {
			l.SessionKind = sshmgr.SessionSNIProxy
		} else {
			l.Rejected = true
			l.Reason = ReasonUnsupportedTLSMode
		}
	default:
		// HTTPS, UDP, anything else.
		l.Rejected = true
		l.Reason = ReasonUnsupportedListenerProtocol
	}
}

func createSSHSessionPool(ctx context.Context) (*sshmgr.SSHSessionPool, error) {
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

	return sshmgr.NewSSHSessionPool(ctx, &sshConfig)
}

func getSvcHostname(svcName, svcNamespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", svcName, svcNamespace)
}

// extractParentGatewayRef returns the namespace and name of the Gateway targeted
// by the route's first ParentRef. It performs only the minimal validation needed
// to identify the parent Gateway (at least one ParentRef with a non-empty Name);
// sectionName, rules and backends are validated separately by the full extractors.
//
// Reconcilers call this first so they can decide whether a route targets a Gateway
// managed by this controller BEFORE running strict validation. This keeps routes
// that belong to other controllers — which legitimately omit the optional
// sectionName — from surfacing as spurious "Reconciler error" log spam.
func extractParentGatewayRef(parentRefs []gatewayv1.ParentReference, routeNamespace string) (gwNamespace, gwName string, err error) {
	if len(parentRefs) < 1 {
		return "", "", fmt.Errorf("route has no ParentRef")
	}
	parent := parentRefs[0]
	if parent.Name == "" {
		return "", "", fmt.Errorf("ParentRef name is empty")
	}
	gwNamespace = routeNamespace
	if parent.Namespace != nil {
		gwNamespace = string(*parent.Namespace)
	}
	return gwNamespace, string(parent.Name), nil
}

// extractRouteDetailsFromParts factors the validation shared by
// extractTCPRouteDetails and extractTLSRouteDetails. kind is the route Kind
// (e.g. "TCPRoute", "TLSRoute") used in error and log messages. ruleCount is
// len(route.Spec.Rules); the caller has already pulled BackendRefs off Rules[0].
func extractRouteDetailsFromParts(
	kind string,
	routeNS string,
	routeName string,
	parentRefs []gatewayv1.ParentReference,
	ruleCount int,
	backendRefs []gatewayv1.BackendRef,
) (*routeDetails, error) {
	routeKey := fmt.Sprintf("%s/%s", routeNS, routeName)
	log := slog.With("kind", kind, "route", routeKey)

	if len(parentRefs) < 1 {
		return nil, fmt.Errorf("%s %s must have at least one ParentRef", kind, routeKey)
	}
	if len(parentRefs) > 1 {
		log.Debug("route has more than one ParentRef, only the first will be used")
	}
	parent := parentRefs[0]

	if parent.Name == "" {
		return nil, fmt.Errorf("ParentRef name is empty for %s %s", kind, routeKey)
	}

	parentNamespace := routeNS
	if parent.Namespace != nil {
		parentNamespace = string(*parent.Namespace)
	} else {
		log.Debug("ParentRef namespace is nil, defaulting to route namespace")
	}

	if parent.SectionName == nil {
		return nil, fmt.Errorf("ParentRef sectionName is nil for %s %s", kind, routeKey)
	}

	if ruleCount < 1 {
		return nil, fmt.Errorf("%s %s must have at least one Rule", kind, routeKey)
	}
	if ruleCount > 1 {
		log.Debug("route has more than one Rule, only the first will be used")
	}

	if len(backendRefs) < 1 {
		return nil, fmt.Errorf("%s %s must have at least one BackendRef", kind, routeKey)
	}
	if len(backendRefs) > 1 {
		log.Debug("Rule has more than one BackendRef, only the first will be used")
	}
	k8Svc := backendRefs[0]

	if k8Svc.Name == "" {
		return nil, fmt.Errorf("BackendRef name is empty for %s %s", kind, routeKey)
	}

	if k8Svc.Port == nil {
		return nil, fmt.Errorf("BackendRef port is nil for %s %s", kind, routeKey)
	}

	if routeNS == "" {
		return nil, fmt.Errorf("%s namespace is nil or empty for %s %s", kind, kind, routeName)
	}

	backendNamespace := routeNS
	if k8Svc.Namespace != nil {
		backendNamespace = string(*k8Svc.Namespace)
	} else {
		log.Debug("BackendRef namespace is nil, defaulting to route namespace")
	}

	return &routeDetails{
		routeName:      routeName,
		routeNamespace: routeNS,
		gwName:         string(parent.Name),
		gwNamespace:    parentNamespace,
		listenerName:   string(*parent.SectionName),
		backendHost:    getSvcHostname(string(k8Svc.Name), backendNamespace),
		backendPort:    int(*k8Svc.Port),
	}, nil
}

// extractTLSRouteDetails extracts common route details from a TLSRoute resource.
func extractTLSRouteDetails(k8sRoute *gatewayv1alpha2.TLSRoute) (*routeDetails, error) {
	var backendRefs []gatewayv1.BackendRef
	if len(k8sRoute.Spec.Rules) > 0 {
		backendRefs = k8sRoute.Spec.Rules[0].BackendRefs
	}
	return extractRouteDetailsFromParts(
		"TLSRoute",
		k8sRoute.Namespace,
		k8sRoute.Name,
		k8sRoute.Spec.ParentRefs,
		len(k8sRoute.Spec.Rules),
		backendRefs,
	)
}

// parseProxyProtocol parses the proxy-protocol annotation value.
// Returns 0 (disabled) for empty/absent, 1 or 2 for valid versions.
// Logs a warning and returns 0 for invalid values.
func parseProxyProtocol(annotations map[string]string) int {
	val, ok := annotations[annotationProxyProtocol]
	if !ok || val == "" {
		return 0
	}
	version, err := strconv.Atoi(val)
	if err != nil || (version != 1 && version != 2) {
		slog.With("function", "parseProxyProtocol").Warn("invalid proxy-protocol annotation value, ignoring",
			"value", val, "valid_values", "1, 2")
		return 0
	}
	return version
}

// parseListenerProxyProtocol returns true iff the Gateway carries the
// ssh-gateway.io/listener-proxy-protocol.<listenerName> annotation set to "true"
// (case-insensitive). Any other non-empty value logs a warning and is treated
// as absent.
func parseListenerProxyProtocol(annotations map[string]string, listenerName string) bool {
	key := annotationListenerProxyProtocolPrefix + listenerName
	val, ok := annotations[key]
	if !ok || val == "" {
		return false
	}
	if strings.EqualFold(val, "true") {
		return true
	}
	slog.With("function", "parseListenerProxyProtocol").Warn(
		"invalid listener-proxy-protocol annotation value, ignoring",
		"listener", listenerName, "value", val, "valid_values", "true")
	return false
}

// remoteAddressPatterns holds the precompiled regexes used by getRemoteAddress.
// Compiled at package init so the per-chunk hot path on SSH stdout/stderr does
// not pay regexp.MustCompile + GC cost on every invocation.
var remoteAddressPatterns = map[string][]*regexp.Regexp{
	"tcp": {
		regexp.MustCompile(`TCP\x1b\[0m:\s+([\w\.-]+:\d+)\r`),
		regexp.MustCompile(`Forwarding TCP traffic from ([\w\.-]+:\d+)`),
	},
	"http": {
		regexp.MustCompile(`HTTP\x1b\[0m:\s+(http://[\w\.-]+)\r`),
		regexp.MustCompile(`Forwarding HTTP traffic from (http://[\w\.-]+)`),
	},
	"https": {
		regexp.MustCompile(`HTTPS\x1b\[0m:\s+(https://[\w\.-]+)\r`),
		regexp.MustCompile(`tunneled with tls termination, (https://[\w\.-]+)`),
		regexp.MustCompile(`Forwarding HTTP traffic from (https://[\w\.-]+)`),
	},
}

// getRemoteAddress extracts remote addresses from the input string using regex.
// pico.sh TCP and HTTP(S) URIs are supported.
// localhost.run HTTPS URIs are supported
// serveo.net TCP and HTTP(S) URIs are supported
func getRemoteAddress(input string) ([]string, error) {
	var results []string
	for scheme, pats := range remoteAddressPatterns {
		for _, re := range pats {
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
