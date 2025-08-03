# Changelog

## [2.0.0] - 2025-09-03

### Changed
- **BREAKING**: Generalize the Gateway API controller to work with any SSH tunneling provider
- **BREAKING**: Remove pico.sh specific references from code and configuration
- **BREAKING**: Change module name from `github.com/dguerri/pico-sh-gateway-api-controller` to `github.com/dguerri/ssh-gateway-api-controller`
- **BREAKING**: Update all Kubernetes resource names to use `ssh-gateway-` prefix instead of `pico-sh-` prefix
- **BREAKING**: Change default controller name from `tunnels.pico.sh/gateway-api-gateway-controller` to `tunnels.ssh.gateway-api-controller`

### Added
- Support for configurable SSH server via `SSH_SERVER` environment variable
- Support for configurable SSH username via `SSH_USERNAME` environment variable  
- Support for configurable controller name via `GATEWAY_CONTROLLER_NAME` environment variable
- Support for SSH host key verification via `SSH_HOST_KEY` environment variable
- Comprehensive documentation for different SSH providers (Pico.sh, Serveo, custom OpenSSH servers)
- Security best practices documentation
- Troubleshooting guide
- Performance tuning recommendations

### Removed
- Hardcoded pico.sh server address (`tuns.sh:22`)
- Hardcoded pico.sh username (`pico-tunnel`)
- Hardcoded pico.sh controller name
- Pico.sh specific documentation and examples

### Updated
- Default SSH server changed from `tuns.sh:22` to `localhost:22`
- Default SSH username changed from `pico-tunnel` to `tunnel-user`
- All Kubernetes manifests updated to use generic names
- Dockerfile labels updated
- README.md completely rewritten with multi-provider support
- Example applications updated to use new naming

## Supported SSH Providers

The controller now supports:

### Pico.sh
- Premium SSH tunneling service with automatic TLS
- Custom domains support
- Multi-region support
- Configuration: `SSH_SERVER=tuns.sh:22`, `SSH_USERNAME=your-pico-username`

### Serveo  
- Free SSH tunneling service
- Configuration: `SSH_SERVER=serveo.net:22`, `SSH_USERNAME=your-serveo-username`

### Custom OpenSSH Servers
- Any SSH server with reverse port forwarding enabled
- Requires `AllowTcpForwarding yes` and `GatewayPorts yes` in sshd_config
- Configuration: `SSH_SERVER=your-ssh-server.com:22`, `SSH_USERNAME=your-ssh-username`

## Migration Guide

### For Existing Users

1. **Update your deployment** to include the new environment variables:
   ```yaml
   env:
   - name: SSH_SERVER
     value: "tuns.sh:22"  # or your preferred SSH server
   - name: SSH_USERNAME
     value: "your-username"
   - name: GATEWAY_CONTROLLER_NAME
     value: "tunnels.pico.sh/gateway-api-controller"  # or your preferred controller name
   ```

2. **Update your GatewayClass** to use the new controller name:
   ```yaml
   apiVersion: gateway.networking.k8s.io/v1
   kind: GatewayClass
   metadata:
     name: ssh-gateway-cl
   spec:
     controllerName: tunnels.pico.sh/gateway-api-controller  # or your preferred controller name
   ```

3. **Update your Gateway resources** to reference the new GatewayClass:
   ```yaml
   spec:
     gatewayClassName: ssh-gateway-cl  # instead of pico-sh-gateway-cl
   ```

4. **Rebuild and redeploy** the controller with the new image name:
   ```bash
   docker build . -t ssh-gateway-api-controller:latest
   ```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SSH_SERVER` | `localhost:22` | SSH server address and port |
| `SSH_USERNAME` | `tunnel-user` | SSH username |
| `SSH_PRIVATE_KEY_PATH` | `/ssh/id` | Path to SSH private key |
| `GATEWAY_CONTROLLER_NAME` | `tunnels.ssh.gateway-api-controller` | Controller name for GatewayClass matching |
| `SSH_HOST_KEY` | `""` | SSH host key fingerprint for verification |
| `CONNECT_TIMEOUT` | `5s` | SSH connection timeout |
| `KEEP_ALIVE_INTERVAL` | `10s` | SSH keepalive interval |
| `BACKOFF_INTERVAL` | `5s` | Reconnection backoff interval |

## Security Improvements

- Added support for SSH host key verification
- Improved error handling and logging
- Better connection management and reconnection logic
- Comprehensive security documentation

## Documentation

- Complete rewrite of README.md with multi-provider support
- Added `docs/ssh-providers.md` with detailed configuration guide
- Added `docs/examples.md` with configuration examples
- Added troubleshooting and security sections 