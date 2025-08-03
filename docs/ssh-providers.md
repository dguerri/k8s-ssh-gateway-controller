# SSH Provider Configuration Guide

This document provides detailed configuration instructions for using the SSH Gateway API controller with different SSH tunneling providers.

## Overview

The SSH Gateway API controller works with any SSH server that supports reverse port forwarding (SSH `-R` flag). This includes commercial services like Pico.sh and Serveo, as well as your own OpenSSH servers.

## Supported Providers

### 1. Pico.sh

[Pico.sh](https://pico.sh/tuns) is a premium SSH tunneling service that provides:
- Automatic TLS termination
- Custom domains
- Multi-region support
- Per-site analytics
- Alerting for tunnel connect/disconnects

#### Configuration

1. **Sign up for Pico.sh** and get your SSH key configured
2. **Update the Kubernetes deployment** with Pico.sh specific settings:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ssh-gateway-api-controller
  namespace: ssh-gateway-system
spec:
  template:
    spec:
      containers:
      - name: controller
        image: your-registry/ssh-gateway-api-controller:latest
        env:
        - name: SSH_SERVER
          value: "tuns.sh:22"
        - name: SSH_USERNAME
          value: "your-pico-username"
        - name: GATEWAY_CONTROLLER_NAME
          value: "tunnels.pico.sh/gateway-api-controller"
        - name: SSH_HOST_KEY
          value: "SHA256:your-host-key-fingerprint"  # Optional
```

3. **Create your SSH key secret**:

```bash
# Encode your Pico.sh SSH key
base64 -w0 < $HOME/.ssh/id_rsa > pico-key.b64

# Create the secret
kubectl create secret generic ssh-gateway-ssh-key \
  --from-file=id=pico-key.b64 \
  --namespace=ssh-gateway-system
```

#### Usage

Your services will be available at:
- `https://your-username-service-name.tuns.sh`
- `https://your-username-service-name.nue.tuns.sh` (region-specific)

#### Custom Domains

Pico.sh supports custom domains. To use them:

1. Set up DNS records:
   ```
   your-domain.com.          300     IN      CNAME   tuns.sh.
   _sish.your-domain.com     300     IN      TXT     "SHA256:your-key-fingerprint"
   ```

2. Update your Gateway listeners to use the custom hostname:
   ```yaml
   listeners:
   - name: http
     protocol: HTTP
     port: 80
     hostname: "your-domain.com"
   ```

### 2. Serveo

[Serveo](https://serveo.net) is a free SSH tunneling service.

#### Configuration

1. **Update the Kubernetes deployment**:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ssh-gateway-api-controller
  namespace: ssh-gateway-system
spec:
  template:
    spec:
      containers:
      - name: controller
        image: your-registry/ssh-gateway-api-controller:latest
        env:
        - name: SSH_SERVER
          value: "serveo.net:22"
        - name: SSH_USERNAME
          value: "your-serveo-username"
        - name: GATEWAY_CONTROLLER_NAME
          value: "tunnels.serveo.net/gateway-api-controller"
```

2. **Create your SSH key secret**:

```bash
# Encode your SSH key
base64 -w0 < $HOME/.ssh/id_rsa > serveo-key.b64

# Create the secret
kubectl create secret generic ssh-gateway-ssh-key \
  --from-file=id=serveo-key.b64 \
  --namespace=ssh-gateway-system
```

#### Usage

Your services will be available at:
- `https://your-service-name.serveo.net`

### 3. Plain OpenSSH Server

You can use any SSH server with reverse port forwarding enabled.

#### SSH Server Requirements

1. **Enable reverse port forwarding** in `/etc/ssh/sshd_config`:
   ```
   AllowTcpForwarding yes
   GatewayPorts yes  # Required for binding to all interfaces
   ```

2. **Restart SSH service**:
   ```bash
   sudo systemctl restart sshd
   ```

3. **Ensure user has permission** to bind to ports (may require sudo or specific port ranges)

#### Configuration

1. **Update the Kubernetes deployment**:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ssh-gateway-api-controller
  namespace: ssh-gateway-system
spec:
  template:
    spec:
      containers:
      - name: controller
        image: your-registry/ssh-gateway-api-controller:latest
        env:
        - name: SSH_SERVER
          value: "your-ssh-server.com:22"
        - name: SSH_USERNAME
          value: "your-ssh-username"
        - name: GATEWAY_CONTROLLER_NAME
          value: "tunnels.ssh.gateway-api-controller"
        - name: SSH_HOST_KEY
          value: "SHA256:your-host-key-fingerprint"  # Recommended for security
```

2. **Create your SSH key secret**:

```bash
# Encode your SSH key
base64 -w0 < $HOME/.ssh/id_rsa > ssh-key.b64

# Create the secret
kubectl create secret generic ssh-gateway-ssh-key \
  --from-file=id=ssh-key.b64 \
  --namespace=ssh-gateway-system
```

#### Usage

Your services will be available at:
- `http://your-ssh-server.com:port` (for HTTP)
- `your-ssh-server.com:port` (for TCP)

## Security Considerations

### SSH Key Security

- **Never use password-protected keys** - The controller cannot handle interactive password prompts
- **Use dedicated keys** - Create SSH keys specifically for the controller, don't reuse personal keys
- **Rotate keys regularly** - Change SSH keys periodically for better security
- **Limit key permissions** - Use keys with minimal required permissions

### Host Key Verification

For production use, always verify SSH host keys:

1. **Get the host key fingerprint**:
   ```bash
   ssh-keyscan -H your-ssh-server.com 2>/dev/null | ssh-keygen -lf -
   ```

2. **Set the host key in the deployment**:
   ```yaml
   env:
   - name: SSH_HOST_KEY
     value: "SHA256:your-host-key-fingerprint"
   ```

### Network Security

- **Use VPNs** when possible for additional security
- **Monitor SSH connections** for unusual activity
- **Limit SSH access** to specific IP ranges if possible
- **Use firewalls** to restrict SSH access

## Troubleshooting

### Common Issues

#### SSH Connection Failed

**Symptoms**: Controller logs show connection errors
```
time=2025-05-07T20:25:14Z level=ERROR msg="ssh connection failed" error="failed to connect to SSH server: dial tcp: connection refused"
```

**Solutions**:
1. Verify SSH server address and port
2. Check SSH server is running and accessible
3. Ensure SSH key format is correct (no password protection)
4. Verify SSH server allows reverse port forwarding

#### Gateway Not Accepted

**Symptoms**: Gateway status shows `Accepted: False`
```
❯ kubectl get gateway
NAME              CLASS            ACCEPTED   PROGRAMMED   AGE
my-gateway        ssh-gateway-cl   False      Unknown      1m
```

**Solutions**:
1. Verify GatewayClass controller name matches deployment
2. Check GatewayClass is accepted
3. Ensure controller is running and healthy

#### Routes Not Working

**Symptoms**: Services not accessible externally
```
❯ curl https://your-service.ssh-provider.com
curl: (7) Failed to connect to your-service.ssh-provider.com port 443: Connection refused
```

**Solutions**:
1. Verify backend services are running
2. Check service selectors and ports
3. Ensure SSH tunnels are established
4. Verify DNS resolution for custom domains

### Debugging

#### Enable Debug Logging

Set the log level to debug in the deployment:

```yaml
env:
- name: LOG_LEVEL
  value: "debug"
```

#### Check Controller Logs

```bash
kubectl logs -f -n ssh-gateway-system ssh-gateway-api-controller-xxx
```

#### Verify SSH Connections

Check if SSH tunnels are established:

```bash
# On your SSH server
netstat -tlnp | grep :80
netstat -tlnp | grep :443
```

#### Test SSH Connection Manually

Test SSH connectivity from the controller pod:

```bash
# Get a shell in the controller pod
kubectl exec -it -n ssh-gateway-system ssh-gateway-api-controller-xxx -- /bin/sh

# Test SSH connection
ssh -i /ssh/id -p 22 your-username@your-ssh-server.com
```

## Performance Tuning

### Connection Settings

Adjust connection timeouts and intervals based on your network:

```yaml
env:
- name: CONNECT_TIMEOUT
  value: "10s"  # Increase for slow networks
- name: KEEP_ALIVE_INTERVAL
  value: "30s"  # Increase for stability
- name: BACKOFF_INTERVAL
  value: "10s"  # Increase for aggressive reconnection
```

### Resource Limits

Adjust resource limits based on your workload:

```yaml
resources:
  limits:
    cpu: 200m      # Increase for high traffic
    memory: 256Mi  # Increase for many connections
  requests:
    cpu: 100m
    memory: 128Mi
```

## Examples

### Complete Pico.sh Configuration

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ssh-gateway-ssh-key
  namespace: ssh-gateway-system
type: Opaque
data:
  id: <base64-encoded-pico-ssh-key>

---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: ssh-gateway-cl
spec:
  controllerName: tunnels.pico.sh/gateway-api-controller

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ssh-gateway-api-controller
  namespace: ssh-gateway-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ssh-gateway-api-controller
  template:
    metadata:
      labels:
        app: ssh-gateway-api-controller
    spec:
      serviceAccountName: ssh-gateway-api-controller-sa
      containers:
      - name: controller
        image: your-registry/ssh-gateway-api-controller:latest
        env:
        - name: SSH_SERVER
          value: "tuns.sh:22"
        - name: SSH_USERNAME
          value: "your-pico-username"
        - name: GATEWAY_CONTROLLER_NAME
          value: "tunnels.pico.sh/gateway-api-controller"
        - name: SSH_HOST_KEY
          value: "SHA256:your-pico-host-key"
        volumeMounts:
        - name: ssh-key
          mountPath: /ssh
          readOnly: true
      volumes:
      - name: ssh-key
        secret:
          secretName: ssh-gateway-ssh-key
          defaultMode: 256
```

### Example Gateway with Custom Domain

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-app-gateway
spec:
  gatewayClassName: ssh-gateway-cl
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    hostname: "my-app.example.com"
    allowedRoutes:
      namespaces:
        from: All
  - name: https
    protocol: HTTPS
    port: 443
    hostname: "my-app.example.com"
    allowedRoutes:
      namespaces:
        from: All
```

This configuration will expose your services at `https://my-app.example.com` when using Pico.sh with a custom domain. 