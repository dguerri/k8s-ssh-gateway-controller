# Configuration Examples

This document provides example configurations for different SSH tunneling providers.

## Pico.sh Configuration

### Deployment with Pico.sh

```yaml
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
          value: "SHA256:your-pico-host-key-fingerprint"
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

### GatewayClass for Pico.sh

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: pico-gateway-cl
spec:
  controllerName: tunnels.pico.sh/gateway-api-controller
```

### Example Gateway

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: my-app-gateway
spec:
  gatewayClassName: pico-gateway-cl
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    hostname: "my-app"
    allowedRoutes:
      namespaces:
        from: All
```

## Serveo Configuration

### Deployment with Serveo

```yaml
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
          value: "serveo.net:22"
        - name: SSH_USERNAME
          value: "your-serveo-username"
        - name: GATEWAY_CONTROLLER_NAME
          value: "tunnels.serveo.net/gateway-api-controller"
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

### GatewayClass for Serveo

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: serveo-gateway-cl
spec:
  controllerName: tunnels.serveo.net/gateway-api-controller
```

## Custom OpenSSH Server Configuration

### Deployment with Custom SSH Server

```yaml
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
          value: "your-ssh-server.com:22"
        - name: SSH_USERNAME
          value: "your-ssh-username"
        - name: GATEWAY_CONTROLLER_NAME
          value: "tunnels.ssh.gateway-api-controller"
        - name: SSH_HOST_KEY
          value: "SHA256:your-host-key-fingerprint"
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

### GatewayClass for Custom SSH Server

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: custom-ssh-gateway-cl
spec:
  controllerName: tunnels.ssh.gateway-api-controller
```

## Complete Application Example

### Application Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: example-app
spec:
  replicas: 3
  selector:
    matchLabels:
      app: example-app
  template:
    metadata:
      labels:
        app: example-app
    spec:
      containers:
      - name: app
        image: nginx:alpine
        ports:
        - containerPort: 80
```

### Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: example-app-service
spec:
  selector:
    app: example-app
  ports:
  - protocol: TCP
    port: 80
    targetPort: 80
```

### Gateway

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: example-gateway
spec:
  gatewayClassName: pico-gateway-cl  # or serveo-gateway-cl or custom-ssh-gateway-cl
  listeners:
  - name: http
    protocol: HTTP
    port: 80
    hostname: "example-app"
    allowedRoutes:
      namespaces:
        from: All
```

### HTTPRoute

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: example-route
spec:
  parentRefs:
  - name: example-gateway
    sectionName: http
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: "/"
    backendRefs:
    - name: example-app-service
      port: 80
```

## Multi-Provider Setup

You can run multiple instances of the controller for different SSH providers:

### Namespace for Pico.sh

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: pico-gateway-system
```

### Namespace for Serveo

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: serveo-gateway-system
```

### Deployments

```yaml
# Pico.sh deployment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pico-gateway-api-controller
  namespace: pico-gateway-system
spec:
  # ... pico.sh configuration

---
# Serveo deployment  
apiVersion: apps/v1
kind: Deployment
metadata:
  name: serveo-gateway-api-controller
  namespace: serveo-gateway-system
spec:
  # ... serveo configuration
```

### GatewayClasses

```yaml
# Pico.sh GatewayClass
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: pico-gateway-cl
spec:
  controllerName: tunnels.pico.sh/gateway-api-controller

---
# Serveo GatewayClass
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: serveo-gateway-cl
spec:
  controllerName: tunnels.serveo.net/gateway-api-controller
```

This allows you to use different SSH providers for different applications or environments. 