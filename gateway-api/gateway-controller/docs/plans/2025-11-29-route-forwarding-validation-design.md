# Route Forwarding Validation Design

**Date:** 2025-11-29
**Status:** Approved

## Problem Statement

When SSH reconnects and the server assigns a different hostname (e.g., subdomain unavailable), forwardings fail but routes appear attached. The controller never retries because:

1. SSH manager correctly rejects wrong hostname and cleans its state
2. Gateway controller's internal state (`l.route`) remains populated
3. Route reconciliation sees `hasExistingRoute=true` and skips validation
4. **Result:** Route appears healthy but has no working forwarding

### Example from Logs

```
time=2025-11-29T09:06:02.178Z level=WARN msg="wrong hostname assigned"
    requested_host=per-la-categoria.guerri.me
    received_uris="[http://dguerri-rem.nue.tuns.sh]"
time=2025-11-29T09:06:02.209Z level=ERROR msg="failed to restore forwarding"

[Later, route reconciles...]
time=2025-11-29T09:05:53.626Z level=DEBUG msg="route already correctly attached, nothing to do"
```

The route thinks it's attached, but SSH manager has no forwarding.

## Design Principles

### 1. Clear Separation of Concerns

```
Route Controllers → Own forwarding lifecycle, validate forwardings work
Gateway Controller → Manage SSH connection health only
SSH Manager → Simple SSH operations, maintain clean state, fail fast
```

### 2. Fail Fast, Let Kubernetes Retry

- All checks are synchronous (no blocking/waiting)
- Return errors immediately on validation failure
- Kubernetes controller-runtime handles exponential backoff
- Periodic reconciliation ensures eventual consistency

### 3. Single Source of Truth

SSH Manager maintains authoritative state:
- `forwardings` map: what we requested
- `assignedAddrs` map: what server assigned
- These are always consistent (cleaned together on disconnect/errors)

## Architecture

### Three-Layer Design

```
┌─────────────────────────────────────────────────────────────┐
│ Route Controllers (HTTPRoute, TCPRoute)                      │
│                                                              │
│ Responsibilities:                                            │
│ - Own forwarding lifecycle                                   │
│ - Validate forwardings are working                           │
│ - Clear stale state when validation fails                    │
│ - Fail fast, let Kubernetes retry                            │
│                                                              │
│ Periodic: Every ROUTE_RECONCILE_PERIOD (default 10s)        │
└─────────────────────────────────────────────────────────────┘
                          ↓ SetRoute()
┌─────────────────────────────────────────────────────────────┐
│ Gateway Controller                                           │
│                                                              │
│ Responsibilities:                                            │
│ - Check IsConnected() periodically                           │
│ - Reconnect SSH if needed                                    │
│ - Provide gateway/listener registry                          │
│ - Provide validation helpers                                 │
│ - Do NOT establish forwardings                               │
│                                                              │
│ Periodic: Every GATEWAY_RECONCILE_PERIOD (default 30s)      │
└─────────────────────────────────────────────────────────────┘
                          ↓ IsConnected(), StartForwarding(),
                            GetAssignedAddresses()
┌─────────────────────────────────────────────────────────────┐
│ SSH Tunnel Manager (simple, stupid)                          │
│                                                              │
│ Responsibilities:                                            │
│ - Connect/disconnect SSH                                     │
│ - Start/stop port forwardings                                │
│ - Maintain clean state                                       │
│ - Validate hostname matches (fail if wrong)                  │
│ - Fail fast on all errors                                    │
│                                                              │
│ State Management (already correct):                          │
│ - closeClient() → clears forwardings + assignedAddrs        │
│ - StartForwarding() → only adds to maps on success          │
│ - StopForwarding() → removes from both maps                 │
└─────────────────────────────────────────────────────────────┘
```

## Solution

### Changes Required

**1. Remove syncAllRoutes() from Gateway Controller**

Currently, after SSH reconnection, Gateway calls `syncAllRoutes()` which tries to restore all forwardings. This creates two code paths establishing forwardings and unclear responsibility.

**Change:** Delete `syncAllRoutes()` entirely. Gateway only reconnects SSH and logs success. Route controllers will detect and restore forwardings via periodic reconciliation (0-10s).

**2. Add Forwarding Validation Helper**

```go
// isForwardingValid checks if a forwarding exists in SSH manager and matches expectations.
// For hostname-based forwardings: verifies assigned URIs contain the requested hostname.
// For wildcard (0.0.0.0): accepts any assigned address.
// Returns false if no addresses assigned or hostname mismatch.
func (r *GatewayReconciler) isForwardingValid(l *Listener) bool {
    addrs := r.manager.GetAssignedAddresses(l.Hostname, l.Port)
    if len(addrs) == 0 {
        return false  // No forwarding exists
    }

    // For specific hostnames (not wildcard), verify hostname matches
    if l.Hostname != "0.0.0.0" {
        for _, addr := range addrs {
            if strings.Contains(addr, l.Hostname) {
                return true  // Found matching hostname in URIs
            }
        }
        return false  // Hostname mismatch (wrong subdomain assigned)
    }

    // For wildcard, any address is valid
    return true
}
```

**3. Update SetRoute() Idempotency Check**

Current logic:
```go
if isRouteAlreadyAttached(...) {
    return nil  // Assume it's working
}
```

New logic:
```go
// Check if route is already correctly attached
if isRouteAlreadyAttached(l, routeName, routeNamespace, backendHost, backendPort) {
    // Validate forwarding still exists and is valid
    if !r.isForwardingValid(l) {
        slog.With("function", "SetRoute").Warn("route attached but forwarding invalid, will retry",
            "gateway", gwKey,
            "route", fmt.Sprintf("%s/%s", routeNamespace, routeName))
        l.route = nil  // Clear stale state
        // Fall through to retry StartForwarding below
    } else {
        slog.With("function", "SetRoute").Debug("route already correctly attached, nothing to do",
            "gateway", gwKey,
            "route", fmt.Sprintf("%s/%s", routeNamespace, routeName))
        return nil
    }
}

// Continue with existing StartForwarding logic...
```

## Recovery Flows

### Scenario 1: SSH Reconnection with Wrong Hostname

```
1. SSH disconnects
   └─ SSH manager clears forwardings + assignedAddrs
   └─ Gateway internal state keeps l.route (stale)

2. Gateway reconciles (30s)
   └─ Detects disconnect via IsConnected()
   └─ Calls Connect()
   └─ SSH reconnected, logs success
   └─ Does NOT call syncAllRoutes() ← KEY CHANGE

3. Route reconciles (within 0-10s)
   └─ Call SetRoute()
   └─ Check: route attached? YES (stale l.route != nil)
   └─ Check: isForwardingValid()? NO (GetAssignedAddresses returns nil)
   └─ Clear: l.route = nil
   └─ StartForwarding() → request per-la-categoria.guerri.me:80

   Option A: Server assigns correct hostname
     └─ Success → route restored ✓

   Option B: Server assigns wrong hostname
     └─ SSH manager rejects (hostname validation)
     └─ Returns error
     └─ SetRoute() returns error
     └─ Controller-runtime exponential backoff
     └─ Retry in 1s, 2s, 4s... until server accepts correct hostname
```

### Scenario 2: Controller Restart with Active Forwardings

```
1. Controller pod restarts
   └─ Internal state cleared (l.route = nil)
   └─ SSH connection persists (forwardings active)

2. Gateway reconciles
   └─ Populates gateway/listener registry (routes still nil)

3. Route reconciles (0-10s)
   └─ Call SetRoute()
   └─ Check: route attached? NO (l.route == nil)
   └─ StartForwarding()
   └─ Returns ErrSSHForwardingExists
   └─ Adoption logic: l.route = populated, return success ✓
```

### Scenario 3: Periodic Reconciliation with Working Route

```
Route reconciles (every 10s)
└─ Call SetRoute()
└─ Check: route attached? YES
└─ Check: isForwardingValid()? YES
└─ Return success (no-op) ✓

No unnecessary SSH operations, efficient.
```

## Benefits

1. **Single Source of Truth**: Only route controllers establish forwardings
2. **Clean Separation**: Gateway owns connection, routes own forwardings
3. **Self-Healing**: Routes automatically detect and fix invalid forwardings
4. **Fail Fast**: Synchronous checks, immediate errors, no blocking
5. **Simple State Management**: SSH manager already maintains consistent state
6. **Kubernetes-Native**: Uses controller-runtime backoff/retry

## Implementation Checklist

- [ ] Add `isForwardingValid()` helper to `gateway_controller.go`
- [ ] Update `SetRoute()` idempotency check to validate forwardings
- [ ] Delete `syncAllRoutes()` function
- [ ] Remove `syncAllRoutes()` call from `Reconcile()`
- [ ] Update `ARCHITECTURE.md` documentation
- [ ] Run tests
- [ ] Verify golangci-lint passes

## Testing Considerations

**Existing tests should pass** - SSH manager state management is already correct.

**Manual validation:**
1. Deploy controller
2. Create Gateway + HTTPRoute
3. Kill SSH server (simulate disconnect)
4. Restart SSH server (will assign different hostname)
5. Observe logs: route should detect invalid forwarding and retry
6. Eventually route should succeed or stay in backoff if hostname never matches

## Documentation Updates

Update `ARCHITECTURE.md`:
- Remove references to `syncAllRoutes()`
- Document three-layer separation of concerns
- Update "SSH Reconnection Workflow" to show route controllers own recovery
- Add "Forwarding Validation" section explaining the checks
