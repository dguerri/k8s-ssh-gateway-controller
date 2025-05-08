# Kubernetes Controllers for pico.sh tunnels

## Disclaimer

This should be considered alpha quality and not ready for production use.

The ingress controller is a prototype and proof of concept (PoC).

## Kubernetes Gateway API Controller using pico.sh tunnels

### How to use the gateway API controller

#### Build the controller container

The controller is not available on any upstream docker registry. You need to build the image and upload it in a registry accessible by your Kubernetes cluster.

For instance:

```sh
cd gateway-api
docker build . -t pico-sh-gateway-api-controller:latest
docker tag pico-sh-gateway-api-controller:latest my-docker-registry.home.me:5001/pico-sh-gateway-api-controller:latest
docker push my-docker-registry.home.me:5001/pico-sh-gateway-api-controller
```

#### Provide missing details in the Kubernetes specification

Edit `gateway-controller/k8s/combined.yaml` and provide:

- The name of the Docker image containing the controller (built above, including the registry) (i.e., `<pico-sh-ingress-controller docker image>`)
- The pico.sh ssh key (i.e., `<base64-encoded-content-of-your-pico-sh-ssh-key>`)

For instance, use this to encode your `id_rsa` key.

```sh
base64 -w0 < $HOME/.ssh/id_rsa
```

WARNING: your key cannot be protected by password.

#### Deploy the controller

The following must be run as a Kubernetes administrator:

```sh
kubectl apply -f gateway-controller/k8s/combined.yaml
```

Verify that everything is working. E.g.,

```sh
❯ kubectl get pods -o wide -n pico-sh-system
NAME                                       READY   STATUS    RESTARTS   AGE   IP             NODE     NOMINATED NODE   READINESS GATES
pico-sh-gateway-api-controller-5976f8677-ggclc   1/1     Running   0          23h     10.50.228.107   k8s-w1   <none>           <none>

❯ kubectl get gatewayclass
NAME                 CONTROLLER                                       ACCEPTED   AGE
pico-sh-gateway-cl   tunnels.pico.sh/gateway-api-gateway-controller   True       10d

❯ kubectl logs -f -n pico-sh-system pico-sh-gateway-api-controller-5976f8677-ggclc
time=2025-05-07T20:25:14.718Z level=INFO msg="starting manager"
time=2025-05-07T20:25:14.718Z level=WARN msg="no host key provided, falling back to InsecureIgnoreHostKey" function=connect
2025-05-07T20:25:14Z    INFO    controller-runtime.metrics      Starting metrics server
2025-05-07T20:25:14Z    INFO    Starting EventSource    {"controller": "httproute", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "HTTPRoute", "source": "kind source: *v1.HTTPRoute"}
2025-05-07T20:25:14Z    INFO    Starting EventSource    {"controller": "tcproute", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "TCPRoute", "source": "kind source: *v1alpha2.TCPRoute"}
2025-05-07T20:25:14Z    INFO    controller-runtime.metrics      Serving metrics server  {"bindAddress": ":8080", "secure": false}
2025-05-07T20:25:14Z    INFO    Starting EventSource    {"controller": "gateway", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "Gateway", "source": "kind source: *v1.Gateway"}
2025-05-07T20:25:14Z    INFO    Starting EventSource    {"controller": "gatewayclass", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "GatewayClass", "source": "kind source: *v1.GatewayClass"}
2025-05-07T20:25:15Z    INFO    Starting Controller     {"controller": "gateway", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "Gateway"}
2025-05-07T20:25:15Z    INFO    Starting workers        {"controller": "gateway", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "Gateway", "worker count": 1}
2025-05-07T20:25:15Z    INFO    Starting Controller     {"controller": "gatewayclass", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "GatewayClass"}
2025-05-07T20:25:15Z    INFO    Starting workers        {"controller": "gatewayclass", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "GatewayClass", "worker count": 1}
2025-05-07T20:25:15Z    INFO    Starting Controller     {"controller": "tcproute", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "TCPRoute"}
2025-05-07T20:25:15Z    INFO    Starting workers        {"controller": "tcproute", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "TCPRoute", "worker count": 1}
2025-05-07T20:25:15Z    INFO    Starting Controller     {"controller": "httproute", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "HTTPRoute"}
2025-05-07T20:25:15Z    INFO    Starting workers        {"controller": "httproute", "controllerGroup": "gateway.networking.k8s.io", "controllerKind": "HTTPRoute", "worker count": 1}
```

#### Run a demo application

Optionally switch to a regular user, with permissions to handle gateway api objects.

```sh
❯ kubectl config use-context kubernetes/davide
Switched to context "kubernetes/davide".
```

Run the following as any user, in any namespace where you have permission to create deployments, services, and ingresses.

```sh
kubectl apply -f example-app/gateway-api/combined.yaml
deployment.apps/tcp-echo-server created
service/tcp-echo-service created
gateway.gateway.networking.k8s.io/tcp-echo-gateway created
tcproute.gateway.networking.k8s.io/tcp-echo-route created
httproute.gateway.networking.k8s.io/http-echo-route created
```

Verify that everything is working:

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
tcp-echo-server-7cbfdb4c5f-kcgcs   1/1     Running   0          5m23s   10.50.46.37     k8s-w2   <none>           <none>
tcp-echo-server-7cbfdb4c5f-r5g76   1/1     Running   0          16s     10.50.228.108   k8s-w1   <none>           <none>
```

Connect to `pico.sh` and check tuns. You should see:

```sh
<your pico username>-web-test.nue.tuns.sh
nue.tuns.sh: 59123
```

Check the endpoints:

```sh
❯ curl -s https://dguerri-web-test.nue.tuns.sh
hello from example-app
❯ nc nue.tuns.sh 59123
GET / HTTP/1.0

HTTP/1.0 200 OK
X-App-Name: http-echo
X-App-Version: 1.0.0
Date: Thu, 08 May 2025 19:41:32 GMT
Content-Length: 23
Content-Type: text/plain; charset=utf-8

hello from example-app
^C
```

#### Clean up

Demo app:

```sh
kubectl delete -f example-app/gateway-api/combined.yaml
```

Controller:

```sh
kubectl delete -f gateway-controller/k8s/combined.yaml
```

## Kubernetes Ingress Controller using pico.sh tunnels

### How to use the ingress controller

#### Build the controller container

The controller is not available on any upstream docker registry. You need to build the image and upload it in a registry accessible by your Kubernetes cluster.

For instance:

```sh
cd ingress-controller
docker build . -t pico-sh-ingress-controller:v1.0
docker tag pico-sh-ingress-controller:v1.0 my-docker-registry.home.me:5001/pico-sh-ingress-controller:latest
docker push my-docker-registry.home.me:5001/pico-sh-ingress-controller
```

#### Provide missing details in the Kubernetes specification

Edit `ingress-controller/k8s/combined.yaml` and provide:

- The name of the Docker image containing the controller (built above) (i.e., `<pico-sh-ingress-controller docker image>`)
- The pico.sh ssh key (i.e., `<base64-encoded-content-of-your-pico-sh-ssh-key>`)

For instance, use this to encode your `id_rsa` key.

```sh
base64 -w0 < $HOME/.ssh/id_rsa
```

WARNING: your key cannot be protected by password.

#### Deploy the controller

The following must be run as a Kubernetes administrator:

```sh
kubectl apply -f ingress-controller/k8s/combined.yaml
```

Verify that everything is working:

```sh
❯ kubectl get pods -o wide -n pico-sh-system
NAME                                       READY   STATUS    RESTARTS   AGE   IP             NODE     NOMINATED NODE   READINESS GATES
pico-sh-ingress-controller-7cc6f8b5dd-9cr6h   1/1     Running   0          16m   10.50.228.71   k8s-w1   <none>           <none>
```

#### Run a demo application

Run the following as any user, in any namespace where you have permission to create deployments, services, and ingresses.

```sh
kubectl apply -f example-app/ingress/combined.yaml
```

Verify that everything is working:

```sh
❯ kubectl get ingress -o wide
NAME          CLASS         HOSTS   ADDRESS   PORTS   AGE
example-app   pico-tunnel   demo              80      14s
```

If, as in this case, you are not using a [custom domain](https://pico.sh/tuns#custom-domains), your actual hostname will be:

```sh
{user}-{host}.tuns.sh
```

or, depending on the region:

```sh
{user}-{host}.nue.tuns.sh
```

For the demo app, if your Pico.sh username is pippo, you can verify that everything works end-to-end with:

```sh
❯ curl https://pippo-demo.nue.tuns.sh
hello from example-app
```

#### Clean up

Demo app:

```sh
kubectl delete -f example-app/ingress/combined.yaml
```

Controller:

```sh
kubectl delete -f ingress-controller/k8s/combined.yaml
```

## How does it work?

TBW.

```
Client Browser
    |
    V
pippo-demo.nue.tuns.sh (DNS to Pico.sh Edge)
    |
    V
SSH Reverse Tunnel (autossh running in your ingress controller Pod)
    |
    V
example-app.default.svc.cluster.local:80 (ClusterIP Service)
    |
    V
Pods selected by label app=example-app
```

## Future improvements

- Replace the ingress controller bash script with a go application

## License

Copyright 2025 Davide Guerri

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.

NOTICE: This project was created by Davide Guerri. Modifications must retain attribution.