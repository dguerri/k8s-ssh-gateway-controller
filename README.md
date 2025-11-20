# Kubernetes Gateway API Controller for SSH Tunneling

## Disclaimer

This should be considered alpha quality and not ready for production use.

## Overview

This Kubernetes Gateway API controller enables exposing Kubernetes services through SSH tunnels. It works with any SSH tunneling provider that supports reverse port forwarding, including:

- **Pico.sh** - A premium SSH tunneling service with automatic TLS
- **Serveo** - A free SSH tunneling service
- **Plain OpenSSH servers** - Any SSH server with reverse port forwarding enabled

## How it works

The controller establishes SSH connections to external SSH servers and creates reverse port forwards (SSH `-R` flag) to expose Kubernetes services. When Gateway API resources are created, the controller:

1. Establishes SSH connections to the configured SSH server
2. Creates reverse port forwards for each listener
3. Routes traffic from the SSH server back to Kubernetes services

```
Client Browser
    |
    V
your-service.ssh-provider.com (DNS to SSH provider)
    |
    V
SSH Reverse Tunnel (controller Pod)
    |
    V
your-service.default.svc.cluster.local:80 (ClusterIP Service)
    |
    V
Pods selected by your service
```

## Supported SSH Providers

### Pico.sh

[Pico.sh](https://pico.sh/tuns) is a premium SSH tunneling service that provides:
- Automatic TLS termination
- Custom domains
- Multi-region support
- Per-site analytics

**Configuration:**
```yaml
env:
- name: SSH_SERVER
  value: "tuns.sh:22"
- name: SSH_USERNAME
  value: "your-pico-username"
- name: GATEWAY_CONTROLLER_NAME
  value: "tunnels.pico.sh/gateway-api-controller"
```

**Usage:**
```bash
# Your services will be available at:
# https://your-username-service-name.tuns.sh
# or https://your-username-service-name.nue.tuns.sh (region-specific)
```

### Serveo

[Serveo](https://serveo.net) is a free SSH tunneling service.

**Configuration:**
```yaml
env:
- name: SSH_SERVER
  value: "serveo.net:22"
- name: SSH_USERNAME
  value: "your-serveo-username"
- name: GATEWAY_CONTROLLER_NAME
  value: "tunnels.serveo.net/gateway-api-controller"
```

**Usage:**
```bash
# Your services will be available at:
# https://your-service-name.serveo.net
```

### Plain OpenSSH Server

You can use any SSH server with reverse port forwarding enabled.

**Configuration:**
```yaml
env:
- name: SSH_SERVER
  value: "your-ssh-server.com:22"
- name: SSH_USERNAME
  value: "your-ssh-username"
- name: GATEWAY_CONTROLLER_NAME
  value: "tunnels.ssh.gateway-api-controller"
```

**SSH Server Requirements:**
- `AllowTcpForwarding yes` in sshd_config
- `GatewayPorts yes` in sshd_config (for binding to all interfaces)
- User must have permission to bind to ports

## Installation

### 1. Build the controller container

The controller is not available on any upstream docker registry. You need to build the image and upload it in a registry accessible by your Kubernetes cluster.

```sh
cd gateway-api/gateway-controller
docker build . -t ssh-gateway-api-controller:latest
docker tag ssh-gateway-api-controller:latest my-docker-registry.home.me:5001/ssh-gateway-api-controller:latest
docker push my-docker-registry.home.me:5001/ssh-gateway-api-controller
```

### 2. Configure the Kubernetes manifests

Edit `gateway-api/k8s/combined.yaml` and provide:

- The name of the Docker image containing the controller (built above, including the registry)
- Your SSH private key (base64-encoded)
- SSH server configuration

For instance, use this to encode your SSH key:

```sh
base64 -w0 < $HOME/.ssh/id_rsa
```

**WARNING:** Your SSH key cannot be protected by password.

### 3. Deploy the controller

The following must be run as a Kubernetes administrator:

```sh
kubectl apply -f gateway-api/k8s/combined.yaml
```

Verify that everything is working:

```sh
❯ kubectl get pods -o wide -n ssh-gateway-system
NAME                                       READY   STATUS    RESTARTS   AGE   IP             NODE     NOMINATED NODE   READINESS GATES
ssh-gateway-api-controller-5976f8677-ggclc   1/1     Running   0          23h     10.50.228.107   k8s-w1   <none>           <none>

❯ kubectl get gatewayclass
NAME                 CONTROLLER                                       ACCEPTED   AGE
ssh-gateway-cl   tunnels.ssh.gateway-api-controller   True       10d

❯ kubectl logs -f -n ssh-gateway-system ssh-gateway-api-controller-5976f8677-ggclc
time=2025-05-07T20:25:14.718Z level=INFO msg="starting manager"
```

## Usage Examples

### Example Application

Run the following as any user, in any namespace where you have permission to create deployments, services, and gateways:

```sh
kubectl apply -f example-apps/gateway-api/combined.yaml
```

This creates:
- A TCP echo server deployment
- A service exposing the echo server
- A Gateway with HTTP and TCP listeners
- HTTPRoute and TCPRoute resources

### Verify the deployment

```sh
❯ kubectl get httproutes.gateway.networking.k8s.io
NAME              HOSTNAMES   AGE
http-echo-route               77s

❯ kubectl get tcproutes.gateway.networking.k8s.io
NAME             AGE
tcp-echo-route   82s

❯ kubectl get pods -o wide
NAME                               READY   STATUS    RESTARTS   AGE     IP              NODE     NOMINATED NODE   READINESS GATES
tcp-echo-server-7cbfdb4c5f-hdmkc   1/1     Running   0          16s     10.50.46.52     k8s-w2   <none>           <none>
```

### Access your services

Depending on your SSH provider, your services will be available at:

**Pico.sh:**
```sh
curl https://your-username-web-test.tuns.sh
nc your-username-web-test.tuns.sh 59123
```

**Serveo:**
```sh
curl https://web-test.serveo.net
nc web-test.serveo.net 59123
```

**Custom SSH server:**
```sh
curl http://your-ssh-server.com:80
nc your-ssh-server.com 59123
```

## Configuration

### Environment Variables

The controller can be configured using the following environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SSH_SERVER` | `localhost:22` | SSH server address and port |
| `SSH_USERNAME` | `tunnel-user` | SSH username |
| `SSH_PRIVATE_KEY_PATH` | `/ssh/id` | Path to SSH private key |
| `GATEWAY_CONTROLLER_NAME` | `tunnels.ssh.gateway-api-controller` | Controller name for GatewayClass matching |
| `CONNECT_TIMEOUT` | `5s` | SSH connection timeout |
| `KEEP_ALIVE_INTERVAL` | `10s` | SSH keepalive interval |
| `BACKOFF_INTERVAL` | `5s` | Reconnection backoff interval |

### SSH Key Requirements

- Must be a private key (RSA, DSA, ECDSA, or Ed25519)
- Cannot be password-protected
- Must be stored as a Kubernetes Secret
- Must have appropriate permissions (0400)

### Gateway API Resources

The controller supports the following Gateway API resources:

- **GatewayClass**: Defines the controller responsible for managing Gateways
- **Gateway**: Defines listeners (ports and protocols) for incoming traffic
- **HTTPRoute**: Routes HTTP traffic to backend services
- **TCPRoute**: Routes TCP traffic to backend services

## Troubleshooting

### Common Issues

1. **SSH Connection Failed**
   - Verify SSH server address and port
   - Check SSH key format and permissions
   - Ensure SSH server allows reverse port forwarding

2. **Gateway Not Accepted**
   - Verify GatewayClass controller name matches
   - Check GatewayClass is accepted

3. **Routes Not Working**
   - Verify backend services are running
   - Check service selectors and ports
   - Ensure SSH tunnels are established

### Debugging

Enable debug logging by setting the log level:

```yaml
env:
- name: LOG_LEVEL
  value: "debug"
```

Check controller logs:

```sh
kubectl logs -f -n ssh-gateway-system ssh-gateway-api-controller-xxx
```

## Development

### Code Quality and Linting

The project uses [golangci-lint](https://golangci-lint.run/) for static code analysis. A comprehensive configuration is provided in [.golangci.yml](.golangci.yml) with 33+ linters enabled.

#### Running Linters

From the `gateway-api/gateway-controller` directory:

```sh
# Run linters
make lint

# Run linters with auto-fix
make lint-fix

# Run all pre-commit checks (format, vet, lint, test)
make pre-commit
```

#### Git Pre-Commit Hook

Install the pre-commit hook to automatically run checks before each commit:

```sh
cd gateway-api/gateway-controller
make install-hooks
```

This will run `fmt`, `vet`, `lint`, and `test` before each commit. To skip the hook:
```sh
git commit --no-verify
```

### Testing

```sh
# Run tests
make test

# Run tests with verbose output
make test-verbose

# Run tests with coverage report
make test-coverage
```

### Building

See [Installation](#installation) section for build instructions.

## Clean up

Demo app:
```sh
kubectl delete -f example-apps/gateway-api/combined.yaml
```

Controller:
```sh
kubectl delete -f gateway-api/k8s/combined.yaml
```

## License

Copyright 2025 Davide Guerri

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.

NOTICE: This project was created by Davide Guerri. Modifications must retain attribution.