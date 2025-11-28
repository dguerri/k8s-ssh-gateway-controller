# Configuration Guide

## Environment Variables

The SSH Gateway API Controller can be configured using the following environment variables:

### SSH Connection Configuration

#### `SSH_SERVER`
- **Description**: SSH server address and port
- **Default**: `localhost:22`
- **Example**: `SSH_SERVER=tunnels.example.com:22`
- **Format**: `hostname:port`

#### `SSH_USERNAME`
- **Description**: Username for SSH authentication
- **Default**: `tunnel-user`
- **Example**: `SSH_USERNAME=myuser`

#### `SSH_PRIVATE_KEY_PATH`
- **Description**: Path to SSH private key file
- **Default**: `/ssh/id`
- **Example**: `SSH_PRIVATE_KEY_PATH=/keys/ssh-key`
- **Note**: Private key must be unencrypted (passphrase not supported)

#### `SSH_HOST_KEY`
- **Description**: SSH host key for server verification (base64 encoded)
- **Default**: `` (empty, falls back to InsecureIgnoreHostKey)
- **Example**: `SSH_HOST_KEY=AAAAB3NzaC1yc2EAAAADAQAB...`
- **Security**: Strongly recommended for production to prevent MITM attacks
- **Warning**: If not provided, SSH connection will use InsecureIgnoreHostKey

### SSH Timeout Configuration

#### `CONNECT_TIMEOUT`
- **Description**: Timeout for establishing SSH connection
- **Default**: `5s`
- **Example**: `CONNECT_TIMEOUT=10s`
- **Format**: Go duration string (e.g., `5s`, `1m`, `500ms`)

#### `KEEP_ALIVE_INTERVAL`
- **Description**: Interval between SSH keepalive messages
- **Default**: `10s`
- **Example**: `KEEP_ALIVE_INTERVAL=15s`
- **Format**: Go duration string
- **Note**: Keepalive timeout is automatically set to 2x this value

### Controller Reconciliation Configuration

#### `GATEWAY_RECONCILE_PERIOD`
- **Description**: How often the Gateway controller reconciles to check SSH connection health and update status
- **Default**: `30s`
- **Example**: `GATEWAY_RECONCILE_PERIOD=60s`
- **Format**: Go duration string
- **Recommended**: 30-60s for most deployments
- **Impact**: Lower values increase API server load but detect SSH issues faster

#### `ROUTE_RECONCILE_PERIOD`
- **Description**: How often HTTPRoute/TCPRoute controllers reconcile to retry failed route attachments
- **Default**: `10s`
- **Example**: `ROUTE_RECONCILE_PERIOD=15s`
- **Format**: Go duration string
- **Recommended**: 10-20s for most deployments
- **Impact**: Lower values provide faster recovery after SSH reconnection but increase reconciliation load

### Controller Identification

#### `GATEWAY_CONTROLLER_NAME`
- **Description**: Controller name used for GatewayClass matching
- **Default**: `tunnels.ssh.gateway-api-controller`
- **Example**: `GATEWAY_CONTROLLER_NAME=example.com/my-ssh-controller`
- **Note**: Must match the `spec.controllerName` in your GatewayClass resources

### Logging Configuration

#### `SLOG_LEVEL`
- **Description**: Logging level for structured logs
- **Default**: `INFO`
- **Values**: `DEBUG`, `INFO`
- **Example**: `SLOG_LEVEL=DEBUG`
- **Note**: DEBUG level is very verbose and should only be used for troubleshooting

---

## Configuration Examples

### Development Configuration

```yaml
env:
  - name: SSH_SERVER
    value: "localhost:2222"
  - name: SSH_USERNAME
    value: "dev-user"
  - name: SLOG_LEVEL
    value: "DEBUG"
  - name: GATEWAY_RECONCILE_PERIOD
    value: "15s"
  - name: ROUTE_RECONCILE_PERIOD
    value: "5s"
```

### Production Configuration

```yaml
env:
  - name: SSH_SERVER
    value: "tunnels.example.com:22"
  - name: SSH_USERNAME
    value: "production-tunnel"
  - name: SSH_HOST_KEY
    valueFrom:
      secretKeyRef:
        name: ssh-config
        key: host-key
  - name: SSH_PRIVATE_KEY_PATH
    value: "/ssh-keys/id_rsa"
  - name: SLOG_LEVEL
    value: "INFO"
  - name: GATEWAY_RECONCILE_PERIOD
    value: "60s"
  - name: ROUTE_RECONCILE_PERIOD
    value: "15s"
  - name: KEEP_ALIVE_INTERVAL
    value: "15s"
  - name: CONNECT_TIMEOUT
    value: "10s"
volumeMounts:
  - name: ssh-keys
    mountPath: /ssh-keys
    readOnly: true
```

### High-Scale Configuration

For deployments with many routes (100+):

```yaml
env:
  - name: GATEWAY_RECONCILE_PERIOD
    value: "60s"     # Reduce API load
  - name: ROUTE_RECONCILE_PERIOD
    value: "20s"     # Reduce reconciliation frequency
  - name: KEEP_ALIVE_INTERVAL
    value: "20s"     # Less frequent keepalives
```

---

## Timing and Recovery Behavior

### SSH Connection Failure Detection

**Timeline**:
1. Keepalive sent every `KEEP_ALIVE_INTERVAL` (default: 10s)
2. Keepalive timeout after `2 * KEEP_ALIVE_INTERVAL` (default: 20s)
3. Connection marked as failed
4. Gateway reconciler detects failure on next cycle (up to `GATEWAY_RECONCILE_PERIOD`, default: 30s)
5. Reconnection attempted

**Total detection time**: 20-50 seconds with defaults

### Route Recovery After SSH Reconnection

**Timeline**:
1. SSH reconnects
2. Gateway runs `syncAllRoutes()` to restore routes in internal state
3. Routes in exponential backoff reconcile on next `ROUTE_RECONCILE_PERIOD` (default: 10s)
4. Routes successfully attach and forwarding resumes

**Total recovery time**: 0-40 seconds with defaults

**Why routes don't reconcile immediately**:
- Routes use periodic reconciliation (every `ROUTE_RECONCILE_PERIOD`)
- This is simpler than annotation-based triggering
- 10-40 second recovery is acceptable for most use cases
- Reduces coupling between Gateway and Route controllers

### Tuning for Faster Recovery

If you need faster recovery, reduce both periods:

```yaml
env:
  - name: GATEWAY_RECONCILE_PERIOD
    value: "15s"
  - name: ROUTE_RECONCILE_PERIOD
    value: "5s"
  - name: KEEP_ALIVE_INTERVAL
    value: "5s"
```

**Recovery time with fast settings**: 10-25 seconds

**Trade-off**: More API calls and reconciliation load

---

## Security Best Practices

### SSH Host Key Verification

**Development** (acceptable):
```yaml
env:
  - name: SSH_HOST_KEY
    value: ""  # Uses InsecureIgnoreHostKey
```

**Production** (required):
```yaml
env:
  - name: SSH_HOST_KEY
    valueFrom:
      secretKeyRef:
        name: ssh-config
        key: host-key
```

### Private Key Management

**Recommended approach**: Use Kubernetes Secrets

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ssh-private-key
type: Opaque
data:
  id_rsa: <base64-encoded-key>
---
apiVersion: v1
kind: Pod
spec:
  containers:
  - name: controller
    volumeMounts:
    - name: ssh-key
      mountPath: /ssh-keys
      readOnly: true
    env:
    - name: SSH_PRIVATE_KEY_PATH
      value: /ssh-keys/id_rsa
  volumes:
  - name: ssh-key
    secret:
      secretName: ssh-private-key
      defaultMode: 0400
```

**Additional security**: Enable Kubernetes secret encryption at rest

---

## Monitoring Recommendations

### Key Metrics to Track

1. **SSH Connection Uptime**
   - Watch for: `"ssh keepalive timeout"` log messages
   - Alert if: Connection fails repeatedly

2. **Route Reconciliation Success Rate**
   - Watch for: `"gateway not ready or not found"` warnings
   - Alert if: Warnings persist beyond `ROUTE_RECONCILE_PERIOD * 3`

3. **Gateway Reconciliation Frequency**
   - Monitor: How often gateways reconcile
   - Tune: Adjust `GATEWAY_RECONCILE_PERIOD` if API load is too high

4. **Route Attachment Failures**
   - Watch for: `"failed to set route"` errors
   - Investigate: Backend service availability, SSH connectivity

### Recommended Alerts

```yaml
# Example Prometheus alert rules
groups:
- name: ssh-gateway-controller
  rules:
  - alert: SSHConnectionDown
    expr: |
      rate(log_messages{level="ERROR", message=~".*ssh keepalive.*"}[5m]) > 0
    for: 2m
    annotations:
      summary: "SSH connection to tunnel server is down"

  - alert: RoutesNotAttaching
    expr: |
      rate(log_messages{level="WARN", message=~".*gateway not ready.*"}[5m]) > 1
    for: 5m
    annotations:
      summary: "Routes failing to attach to gateway"
```

---

## Troubleshooting

### Routes Not Attaching After SSH Reconnection

**Symptoms**: `"listener has no route assigned"` in logs after reconnection

**Diagnosis**:
1. Check route reconciliation period: `echo $ROUTE_RECONCILE_PERIOD`
2. Watch for route reconciliation: `kubectl logs -f <pod> | grep "reconciling HTTPRoute"`
3. Check if routes are in backoff: Look for repeated `"gateway not ready"` warnings

**Solution**:
- Wait up to `ROUTE_RECONCILE_PERIOD` for routes to reconcile
- If routes still don't attach, check Gateway status: `kubectl get gateway -o yaml`
- Manually trigger reconciliation: `kubectl annotate httproute <name> reconcile=now`

### Slow SSH Failure Detection

**Symptoms**: Takes minutes to detect SSH connection failure

**Diagnosis**:
1. Check keepalive interval: `echo $KEEP_ALIVE_INTERVAL`
2. Check if keepalive timeout is working: Look for `"ssh keepalive timeout"` logs

**Solution**:
- Reduce keepalive interval: `KEEP_ALIVE_INTERVAL=5s`
- Verify keepalive timeout fix is deployed (should see timeout errors, not hangs)

### High API Server Load

**Symptoms**: Many reconciliation events, high CPU usage

**Diagnosis**:
1. Check reconciliation periods
2. Count number of Gateways and Routes
3. Monitor API call rate

**Solution**:
- Increase reconciliation periods:
  ```yaml
  GATEWAY_RECONCILE_PERIOD=60s
  ROUTE_RECONCILE_PERIOD=20s
  ```
- For 100+ routes, consider:
  - `GATEWAY_RECONCILE_PERIOD=90s`
  - `ROUTE_RECONCILE_PERIOD=30s`

### "Forwarding already exists" Errors After Controller Restart

**Symptoms**: Logs show `"forwarding already exists"` errors after controller pod restarts

**What's happening**: This is normal and self-healing:
- Controller pod restarts → internal route registry is cleared
- SSH connection persists → forwardings remain active in SSH manager
- Routes reconcile → detect forwardings already exist
- Controller adopts existing forwardings and populates internal state

**Expected behavior**:
```
level=DEBUG msg="found listener for route" hasExistingRoute=false
level=INFO msg="forwarding already exists, adopting it"
```

**Action required**: None - routes will be adopted automatically on next reconciliation

**When to investigate**:
- If adoption fails (ERROR logs instead of INFO)
- If routes remain in backoff after multiple reconciliation cycles
- If forwarding behavior is incorrect after adoption

---

## Default Values Summary

| Variable | Default | Min Recommended | Max Recommended |
|----------|---------|-----------------|-----------------|
| `GATEWAY_RECONCILE_PERIOD` | 30s | 15s | 120s |
| `ROUTE_RECONCILE_PERIOD` | 10s | 5s | 60s |
| `KEEP_ALIVE_INTERVAL` | 10s | 5s | 30s |
| `CONNECT_TIMEOUT` | 5s | 3s | 30s |

---

*Last Updated: 2025-11-28*
