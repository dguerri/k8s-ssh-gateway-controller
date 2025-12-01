# SSH Gateway API Controller - Architecture Documentation

## Overview

The SSH Gateway API Controller is a Kubernetes controller that implements the Gateway API specification to expose services through SSH tunnels. It uses reverse SSH forwarding to expose internal Kubernetes services via an external SSH server (e.g., pico.sh, tuns.sh).

## Component Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                       │
│                                                             │
│  ┌────────────────┐  ┌──────────────┐  ┌─────────────────┐  │
│  │ GatewayClass   │  │   Gateway    │  │ HTTPRoute/      │  │
│  │                │  │              │  │ TCPRoute        │  │
│  └────────────────┘  └──────────────┘  └─────────────────┘  │
│          │                   │                   │          │
│          ▼                   ▼                   ▼          │
│  ┌──────────────────────────────────────────────────────┐   │
│  │         Gateway API Controller (main.go)             │   │
│  │                                                      │   │
│  │  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐  │   │
│  │  │   Gateway    │ │  HTTPRoute   │ │  TCPRoute    │  │   │
│  │  │  Reconciler  │ │  Reconciler  │ │  Reconciler  │  │   │
│  │  └──────────────┘ └──────────────┘ └──────────────┘  │   │
│  │         │                 │                │         │   │
│  │         └─────────────────┴────────────────┘         │   │
│  │                           │                          │   │
│  │                   ┌───────▼────────┐                 │   │
│  │                   │ SSH Tunnel     │                 │   │
│  │                   │ Manager        │                 │   │
│  │                   └───────┬────────┘                 │   │
│  └───────────────────────────┼──────────────────────────┘   │
│                              │ SSH Connection               │
└──────────────────────────────┼──────────────────────────────┘
                               │
                               ▼
                  ┌───────────────────────┐
                  │   SSH Server          │
                  │  (pico.sh, tuns.sh)   │
                  └───────────────────────┘
                              │
                              ▼
                      Internet Traffic
```

## Architecture: Separation of Concerns

The controller follows a clean three-layer architecture where each layer has a single, well-defined responsibility:

```
┌─────────────────────────────────────────────────────────────┐
│ Route Controllers (HTTPRoute, TCPRoute)                      │
│ Responsibility: Own forwarding lifecycle                     │
│ - Ensure forwardings exist and are valid                     │
│ - Validate assigned addresses match expectations             │
│ - Fail fast, return errors for Kubernetes to retry           │
│ - Clear stale state when validation fails                    │
└─────────────────────────────────────────────────────────────┘
                          ↓ SetRoute(), RemoveRoute()
┌─────────────────────────────────────────────────────────────┐
│ Gateway Controller                                           │
│ Responsibility: SSH connection health                        │
│ - Check IsConnected() periodically                           │
│ - Reconnect SSH when needed                                  │
│ - Provide gateway/listener registry                          │
│ - Provide forwarding validation helpers                      │
│ - Do NOT establish forwardings directly                      │
└─────────────────────────────────────────────────────────────┘
                          ↓ IsConnected(), StartForwarding(),
                            GetAssignedAddresses()
┌─────────────────────────────────────────────────────────────┐
│ SSH Tunnel Manager                                           │
│ Responsibility: Simple SSH operations                        │
│ - Connect/disconnect SSH                                     │
│ - Start/stop port forwardings                                │
│ - Validate hostname matches (fail if wrong)                  │
│ - Maintain clean state (forwardings, assignedAddrs)          │
│ - Fail fast on all errors                                    │
│ - No knowledge of Kubernetes or controllers                  │
└─────────────────────────────────────────────────────────────┘
```

**Key Principle:** Single source of truth for each concern. Gateway owns connection, routes own forwardings, SSH manager owns SSH primitives.

## Core Components

### 1. Main Entry Point (`main.go`)

**Responsibility**: Application bootstrap and controller registration

**Key Functions**:
- Configures structured logging (slog) with DEBUG or INFO level
- Creates controller-runtime manager
- Registers three controllers: Gateway, HTTPRoute, TCPRoute
- Establishes inter-controller dependencies (route controllers reference gateway controller)

**Configuration**:
- `SLOG_LEVEL`: Set to "DEBUG" for verbose logging

---

### 2. Gateway Reconciler (`controllers/gateway_controller.go`)

**Responsibility**: Manages Gateway resources and SSH connection lifecycle

#### Internal Data Structures

```go
type GatewayReconciler struct {
    manager    SSHTunnelManagerInterface  // SSH tunnel manager
    Client     client.Client               // Kubernetes API client
    Scheme     *runtime.Scheme
    gateways   map[string]*gateway         // Internal gateway registry
    gatewaysMu sync.Mutex
}

type gateway struct {
    listeners   map[string]*Listener
    listenersMu sync.RWMutex
}

type Listener struct {
    route    *Route      // Currently attached route (nil if none)
    Protocol string
    Hostname string      // Remote hostname for SSH forwarding
    Port     int         // Remote port for SSH forwarding
}

type Route struct {
    Name      string
    Namespace string
    Host      string     // Backend service hostname
    Port      int        // Backend service port
}
```

#### Key Workflows

**1. Gateway Reconciliation Loop** (every `GATEWAY_RECONCILE_PERIOD`, default: 30s)

```
Reconcile(gateway) →
├─ Check SSH connection
│  └─ If disconnected →
│     ├─ Attempt reconnection
│     └─ syncAllRoutes()  // Restore routes in internal state
│                          // Routes will self-reconcile via ROUTE_RECONCILE_PERIOD
│
├─ Validate GatewayClass
├─ Handle finalizers
├─ Update internal gateway/listener registry
└─ Update Gateway status with assigned addresses
```

**2. SSH Reconnection Workflow**

```
SSH Disconnect Detected →
├─ SSH manager closeClient()
│  ├─ Clear assigned addresses
│  └─ Clear forwarding state
│
└─ Gateway reconciles (within 0-30s) →
   ├─ Detects disconnect via IsConnected()
   ├─ Calls Connect()
   └─ SSH reconnected, logs success
      └─ Route controllers restore forwardings via periodic reconciliation (0-10s)
```

**Separation of concerns:** Gateway owns connection, routes own forwardings.

**3. Controller Restart Recovery**

```
Controller Pod Restarts →
├─ Internal state cleared (gateway registry lost)
├─ SSH connection persists (forwardings still active)
│
└─ On startup →
   ├─ Gateway reconciles → populates gateway/listener registry (routes = nil)
   └─ Routes reconcile (periodic) →
      └─ Call SetRoute()
         ├─ Internal state shows: route = nil (lost)
         ├─ SSH manager shows: forwarding exists
         └─ StartForwarding() returns ErrSSHForwardingExists
            └─ Adopt existing forwarding:
               ├─ Populate internal route state
               ├─ Log: "forwarding already exists, adopting it"
               └─ Return success
```

**Recovery time**: 0-10s (up to one route reconciliation period)

**4. Route Attachment with Forwarding Validation (SetRoute)**

```
SetRoute(gateway, listener, route, backend) →
├─ Find gateway in internal registry
├─ Find listener in gateway
│
├─ Idempotency check with validation:
│  ├─ Route already attached? YES
│  ├─ Validate: isForwardingValid()?
│  │  ├─ Check GetAssignedAddresses() returns URIs
│  │  └─ For hostname-based: verify hostname matches
│  ├─ If valid → return success (no-op) ✓
│  └─ If invalid → clear l.route, fall through to retry
│
├─ If listener has different route:
│  └─ StopForwarding(old route)
│
├─ Check SSH connection status
├─ StartForwarding(new route)
│  ├─ If forwarding succeeds → store route, update status ✓
│  ├─ If ErrSSHClientNotReady → return error (gateway not ready, backoff)
│  ├─ If ErrSSHForwardingExists → adopt existing forwarding ✓
│  └─ If wrong hostname → return error (backoff, retry later)
│
├─ Store route in listener
└─ Update Gateway status with assigned addresses
```

**Key improvement:** Validates forwardings on every reconciliation, detects stale state.

**4. Gateway Status Management**

```
updateGatewayAddresses(gateway) →
├─ For each listener:
│  ├─ Get assigned addresses from SSH manager
│  └─ Extract hostname from URI
│
└─ Compare old vs new addresses
   └─ Only update Kubernetes status if changed
```

#### Important Methods

- `SetRoute()`: Attaches a route to a listener (called by route controllers)
  - Validates forwarding exists before claiming route is attached
  - Clears stale state if forwarding is invalid
  - Adopts existing SSH forwardings when controller restarts
  - Handles idempotency: no-op if route already correctly attached AND forwarding valid
- `RemoveRoute()`: Detaches a route from a listener
- `isForwardingValid()`: Validates that SSH manager has valid forwarding for listener
  - Checks GetAssignedAddresses() returns URIs
  - For hostname-based: verifies hostname matches
  - For wildcard (0.0.0.0): accepts any assigned address
- `updateGatewayAddresses()`: Extracts SSH-assigned URIs and populates Gateway status

---

### 3. HTTPRoute Reconciler (`controllers/httproute_controller.go`)

**Responsibility**: Manages HTTPRoute resources

#### Reconciliation Workflow

```
Reconcile(httpRoute) →
├─ Extract route details from spec
│  ├─ parentRef (gateway name, namespace, listener)
│  ├─ backendRef (service name, port)
│  └─ Validate all required fields
│
├─ If being deleted:
│  ├─ GatewayReconciler.RemoveRoute()
│  └─ Remove finalizer
│
└─ If being added/updated:
   ├─ Add finalizer
   ├─ GatewayReconciler.SetRoute()
   │  ├─ If gateway not ready → return error (exponential backoff)
   │  ├─ If gateway not found → return error (exponential backoff)
   │  └─ If success → route is now attached
   └─ Done
```

#### Reconciliation Strategy

**Periodic Reconciliation**:
- Reconciles every `ROUTE_RECONCILE_PERIOD` (default: 10s)
- Ensures routes are always active and retry failures
- Detects SSH manager state changes without explicit triggers

**Exponential Backoff for Errors**:
When `SetRoute()` fails with `ErrGatewayNotReady` or `ErrGatewayNotFound`:
- **Return the error** to trigger controller-runtime's exponential backoff
- Backoff increases: 1s → 2s → 4s → 8s → ... (max ~5 minutes)
- Periodic reconciliation overrides backoff to ensure eventual consistency

**Why exponential backoff?**
- Prevents aggressive retrying when gateway is down
- Reduces API server load
- Periodic reconciliation ensures eventual recovery without tight coupling

#### Validation Rules

- **Required**: At least one ParentRef
- **Limitation**: Only first ParentRef used (logs debug if multiple)
- **Required**: ParentRef.SectionName (listener name)
- **Required**: At least one Rule with BackendRef
- **Limitation**: Only first Rule and first BackendRef used

---

### 4. TCPRoute Reconciler (`controllers/tcproute_controller.go`)

**Responsibility**: Manages TCPRoute resources

**Similarities to HTTPRoute**:
- Same reconciliation logic
- Same exponential backoff strategy
- Same validation rules

**Differences**:
- Uses Gateway API v1alpha2 (TCPRoute is still alpha)
- No hostname matching in routes (TCP is port-based)

---

### 5. SSH Tunnel Manager (`ssh/ssh_tunnel_manager.go`)

**Responsibility**: Manages SSH connections and port forwardings

#### Internal State

```go
type SSHTunnelManager struct {
    client                     sshClient
    connectionCtx              context.Context
    connectionCancel           context.CancelFunc
    forwardings                map[string]*ForwardingConfig  // key: "hostname:port"
    assignedAddrs              map[string][]string           // key → URIs
    addrNotifications          map[string]chan []string
    connected                  bool
    clientMu                   sync.RWMutex
    addrNotifMu                sync.Mutex

    // Configuration
    sshServerAddress           string
    sshUser                    string
    hostKey                    string
    signer                     ssh.Signer
    keepAliveInterval          time.Duration  // Default: 10s
    connTimeout                time.Duration  // Default: 5s
    fwdReqTimeout              time.Duration  // Default: 2s
    addressVerificationTimeout time.Duration  // Default: 30s
}
```

#### Key Workflows

**1. SSH Connection Lifecycle**

```
Connect() →
├─ connectClient()
│  ├─ Build SSH ClientConfig
│  ├─ Dial SSH server
│  ├─ Set connected = true
│  └─ Create connection context
│
├─ Start background goroutines:
│  ├─ handleChannels()      // Accept incoming connections
│  └─ monitorConnection()   // Send keepalives
│
└─ If remoteAddrFunc set:
   └─ captureServerOutput()  // Parse SSH server messages for URIs
```

**2. Keepalive Monitoring** (CRITICAL FIX)

```
monitorConnection() →
├─ Every 10 seconds (configurable):
│  ├─ Send keepalive in goroutine
│  └─ Wait with timeout (2x keepalive interval = 20s)
│     ├─ If response received → continue
│     ├─ If timeout → closeClient() and exit
│     └─ If error → closeClient() and exit
│
└─ On connection close:
   └─ Stop monitoring
```

**Why the timeout is critical**:
- **Without timeout**: If network is blocked (firewall), `SendRequest()` hangs indefinitely waiting for TCP timeout (can be minutes)
- **With timeout**: Connection is detected as failed within 20 seconds
- **Result**: Fast reconnection instead of lingering in broken state

**3. Port Forwarding**

```
StartForwarding(remoteHost, remotePort, backendHost, backendPort) →
├─ Check SSH connected
├─ Check forwarding doesn't already exist
├─ sendForwardingOnce()
│  ├─ Send "tcpip-forward" request
│  └─ Wait for SSH server response (accepted/rejected)
│
├─ If remoteAddrFunc set:
│  ├─ Wait for address notification (from captureServerOutput)
│  ├─ Verify assigned address matches requested hostname
│  └─ Timeout after 30 seconds if no match
│
└─ Store forwarding in registry
```

**4. Address Capture and Verification**

```
captureServerOutput() →
├─ Read SSH server stdout
├─ Extract URIs using regex:
│  ├─ HTTP: http://hostname
│  ├─ HTTPS: https://hostname
│  └─ TCP: tcp://hostname:port
│
├─ Notify waiting forwardings
└─ Store in assignedAddrs
```

**5. Channel Handling** (Incoming Connections)

```
handleChannels() →
├─ For each incoming "forwarded-tcpip" channel:
│  ├─ Parse remote and origin addresses
│  ├─ Find matching forwarding by remote address
│  ├─ Dial backend service (e.g., service.namespace.svc.cluster.local:80)
│  ├─ Accept SSH channel
│  └─ Bidirectionally copy data:
│     ├─ SSH channel ↔ Backend connection
│     └─ Close both when either ends
│
└─ On context cancel:
   └─ Stop accepting channels
```

#### Connection Cleanup

```
closeClient() →
├─ Set connected = false
├─ Cancel connection context
├─ Close SSH client
├─ Clear assigned addresses
└─ Clear forwardings  // Need to be re-established
```

---

## Configuration

### Environment Variables

All configurable via environment variables:

#### SSH Connection
- `SSH_SERVER`: SSH server address (default: `localhost:22`)
- `SSH_USERNAME`: SSH username (default: `tunnel-user`)
- `SSH_PRIVATE_KEY_PATH`: Path to SSH private key (default: `/ssh/id`)
- `SSH_HOST_KEY`: SSH host key for verification (optional, falls back to InsecureIgnoreHostKey)

#### Timeouts
- `KEEP_ALIVE_INTERVAL`: SSH keepalive interval (default: `10s`)
- `CONNECT_TIMEOUT`: SSH connection timeout (default: `5s`)

#### Controller
- `GATEWAY_CONTROLLER_NAME`: Controller name for GatewayClass matching (default: `tunnels.ssh.gateway-api-controller`)
- `SLOG_LEVEL`: Logging level (`DEBUG` or `INFO`)

---

## Data Flow: Complete Route Setup

```
1. User creates Gateway resource
   └─> GatewayReconciler triggered
       ├─> Creates internal gateway + listeners
       ├─> Connects SSH if not connected
       └─> Updates Gateway status (conditions)

2. User creates HTTPRoute resource
   └─> HTTPRouteReconciler triggered
       ├─> Extracts route details (parent, backend)
       ├─> Calls GatewayReconciler.SetRoute()
       │   ├─> Finds gateway and listener
       │   ├─> Calls SSHTunnelManager.StartForwarding()
       │   │   ├─> Sends "tcpip-forward" request to SSH server
       │   │   └─> Waits for URI assignment
       │   └─> Stores route in listener
       └─> GatewayReconciler.updateGatewayStatus()
           └─> Gateway.status.addresses populated with URIs

3. External traffic arrives
   └─> SSH Server receives connection
       └─> Sends "forwarded-tcpip" channel to client
           └─> SSHTunnelManager.handleChannels()
               ├─> Dials backend service in Kubernetes
               └─> Proxies bidirectional traffic
```

---

## Error Handling

### Controller-Level Errors

1. **ErrGatewayNotReady**: SSH not connected
   - **Route Controller Response**: Return error → exponential backoff
   - **Resolution**: Gateway controller will reconnect and trigger route reconciliation

2. **ErrGatewayNotFound**: Gateway not in internal registry
   - **Route Controller Response**: Return error → exponential backoff
   - **Resolution**: Gateway controller will populate registry on next reconcile

3. **ErrRouteNotFound**: Route not in listener (during removal)
   - **Route Controller Response**: Ignore error, remove finalizer
   - **Reason**: Already cleaned up

### SSH-Level Errors

1. **ErrSSHClientNotReady**: SSH client disconnected
   - **Forwarding Response**: Fail immediately
   - **Resolution**: Gateway controller monitors connection, will reconnect

2. **ErrSSHForwardingExists**: Forwarding already active
   - **Forwarding Response**: Adopt existing forwarding
   - **Reason**: Controller restart cleared internal state but SSH forwarding persisted
   - **Recovery**: Populate internal route state and treat as success
   - **Common scenario**: Controller pod restart with persistent SSH connection

3. **ErrSSHConnectionFailed**: TCP connection to SSH server failed
   - **Connection Response**: Log error, set connected=false
   - **Resolution**: Gateway controller will retry after 10 seconds

4. **Keepalive Timeout**: No keepalive response within 20 seconds
   - **Monitor Response**: Close connection
   - **Resolution**: Gateway controller detects disconnect on next reconcile

---

## Synchronization and Concurrency

### Locking Strategy

**GatewayReconciler**:
- `gatewaysMu`: Protects `gateways` map
- Per-gateway `listenersMu`: Protects listeners and routes within gateway
- **Lock ordering**: Always acquire gatewaysMu before listenersMu to prevent deadlocks

**SSHTunnelManager**:
- `clientMu`: Protects connection state and forwardings
- `addrNotifMu`: Protects address notifications and assignments
- **Read/Write locks**: Uses RWMutex for concurrent reads (IsConnected, GetAssignedAddresses)

### Lock Release for API Calls

```go
// SetRoute releases locks before K8s API call
gw.listenersMu.Unlock()
r.gatewaysMu.Unlock()

r.updateGatewayStatus(ctx, gwNamespace, gwName) // No locks held

r.gatewaysMu.Lock()
gw.listenersMu.Lock()
```

**Why?** Kubernetes API calls can be slow. Holding locks during API calls blocks all other operations.

---

## Reconciliation Triggers

### Gateway Reconciler
- **Periodic**: Every `GATEWAY_RECONCILE_PERIOD` (default: 30s)
- **On Gateway changes**: Spec changes (via `GenerationChangedPredicate`)
- **Manual**: kubectl annotate, kubectl edit

### HTTPRoute/TCPRoute Reconcilers
- **Periodic**: Every `ROUTE_RECONCILE_PERIOD` (default: 10s)
- **On route changes**: Any change to the HTTPRoute/TCPRoute resource
- **Manual**: kubectl annotate, kubectl edit

### Why Periodic Reconciliation?

**For Gateways**:
- Monitors SSH connection health every cycle
- Updates Gateway status.addresses when SSH URIs change
- Reconnects SSH if connection is lost

**For Routes**:
- Retries failed route attachments automatically
- Detects SSH manager state changes (reconnection, controller restart)
- Ensures routes are always attached even after transient failures
- Simple and reliable without needing tight coupling between controllers

---

## Critical Code Paths

### 1. SSH Keepalive Timeout (ssh/ssh_tunnel_manager.go:205-234)

**Purpose**: Detect network failures quickly

**Implementation**:
```go
// Run keepalive in goroutine to enable timeout
go func() {
    _, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
    resultCh <- keepaliveResult{err: err}
}()

// Wait with timeout
select {
case result := <-resultCh:
    // Process response
case <-time.After(keepaliveTimeout):
    // Timeout → close connection
}
```

**Impact**: Without this, blocked connections would hang for minutes before detecting failure.

---

### 2. Gateway Address Comparison (gateway_controller.go:447-470)

**Purpose**: Prevent unnecessary Kubernetes API writes

**Implementation**:
```go
oldAddresses := k8sGw.Status.Addresses
r.updateGatewayAddresses(&k8sGw)

if !gatewayAddressesEqual(oldAddresses, k8sGw.Status.Addresses) {
    // Only update if changed
    r.Status().Update(ctx, &k8sGw)
}
```

**Impact**: Reduces API server load during periodic reconciliation.

---

### 3. Route Reconciliation Trigger (gateway_controller.go:417-424)

**Purpose**: Ensure routes reconcile after SSH reconnection

**Implementation**:
```go
// After SSH reconnects
r.syncAllRoutes()                               // Restore routes in internal state
r.triggerRouteReconciliation(ctx, req.Namespace, req.Name)  // Trigger all routes
```

**Impact**: Without this, routes would stay in exponential backoff after reconnection.

---

## Testing Strategy

### Unit Tests

**SSH Tunnel Manager** (`ssh/ssh_tunnel_manager_test.go`):
- Uses fake SSH client and fake net.Dial
- Tests connection lifecycle, forwarding, keepalive, address verification
- Validates timeout behavior

**Gateway Controller** (`controllers/gateway_controller_test.go`):
- Uses fake SSH tunnel manager
- Tests gateway/listener/route management
- Tests address comparison logic

**Route Controllers** (`controllers/*route_controller_test.go`):
- Tests route extraction and validation
- Tests error handling and retries

### Dependency Injection for Testing

```go
// SSH Manager
config.SSHDialFunc = fakeSSHDial  // Inject fake SSH client
config.NetDialFunc = fakeNetDial  // Inject fake TCP dial

// Controllers
osReadFile = fakeReadFile  // Inject fake file reader
```

---

## Operational Considerations

### Monitoring

**Key Metrics to Monitor**:
- SSH connection uptime
- Keepalive success/failure rate
- Route reconciliation errors (from logs)
- Gateway status update frequency
- Forwarding setup/teardown rate

**Important Log Patterns**:
- `"ssh keepalive timeout"` → Network issue or SSH server unresponsive
- `"gateway not ready or not found"` → Routes waiting for gateway
- `"route sync completed"` → Shows routes restored after reconnection
- `"triggered route reconciliation"` → Confirms annotation-based triggers working

### Scaling Considerations

**Current Limitations**:
- Single SSH connection per controller instance
- All forwardings share one SSH connection
- Gateway registry is in-memory (lost on pod restart)

**Scaling Strategies**:
- One controller instance per SSH server (shard by gateway)
- Keep gateway count low (< 50 per instance)
- Use multiple SSH servers for high availability

### Failure Scenarios

**SSH Server Down**:
- Controller logs connection errors every 10 seconds
- Routes remain in exponential backoff
- On SSH recovery: Gateway reconnects, triggers all routes

**Controller Pod Restart**:
- Internal gateway registry rebuilt from Kubernetes state
- Routes reconcile and re-attach (may cause brief downtime)
- SSH connection re-established

**Network Partition**:
- Keepalive timeout detects failure within 20 seconds
- Controller attempts reconnection every 30 seconds
- Routes trigger automatically on successful reconnection

---

## Future Improvements

### Potential Enhancements

1. **Status Reporting**: Populate HTTPRoute/TCPRoute status with route conditions
2. **Metrics Exposure**: Prometheus metrics for monitoring
3. **Multi-Gateway Support**: Multiple SSH connections per controller
4. **Address Caching**: Persist assigned addresses across restarts
5. **Leader Election**: Support multiple controller replicas with leader election
6. **Graceful Shutdown**: Clean up forwardings before terminating

### Known Limitations

1. **Single ParentRef**: Only first ParentRef used in routes (Gateway API supports multiple)
2. **Single Rule**: Only first rule in HTTPRoute/TCPRoute processed
3. **Single BackendRef**: Only first backend used (no load balancing)
4. **No TLS**: HTTPRoute TLS configuration not implemented
5. **No Path Matching**: HTTPRoute path/header matching not used (forwarding is port-based)
6. **GatewayClass Filter**: Only processes gateways with matching controllerName

---

## Glossary

**Gateway**: Kubernetes resource defining network entry point with listeners
**Listener**: Gateway component defining protocol/port/hostname for traffic
**Route**: HTTPRoute or TCPRoute defining traffic routing rules
**Backend**: Kubernetes Service that receives forwarded traffic
**Forwarding**: SSH reverse port forwarding from remote server to backend
**Assigned Address**: URI provided by SSH server for accessing forwarding
**Reconciliation**: Controller loop that ensures desired state matches actual state
**Finalizer**: Kubernetes mechanism to ensure cleanup before resource deletion
**ParentRef**: Reference from route to gateway/listener
**BackendRef**: Reference from route to backend service

---

*Last Updated: 2025-11-28*
