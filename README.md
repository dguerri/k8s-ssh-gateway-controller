# Kubernetes Ingress Controller using pico.sh tunnels

## Disclaimer

This should be considered experimental and not ready for production use.

The controller is a prototype and proof of concept (PoC).

## How to use

### Build the controller container

The controller is not available on any upstream docker registry. You need to build the image and upload it in a registry accessible by your Kubernetes cluster.

For instance:

```sh
cd controller
docker build . -t pico-ingress-controller:v1.0
docker tag pico-ingress-controller:v1.0 my-docker-registry.home.me:5001/pico-ingress-controller:v1.0
docker push my-docker-registry.home.me:5001/pico-ingress-controller:v1.0
```

### Provide missing details in the Kubernetes specification

Edit `k8s/combined.yaml` and provide:

- The name of the Docker image containing the controller (built above) (i.e., `<pico-ingress-controller docker image>`)
- The pico.sh ssh key (i.e., `<base64-encoded-content-of-your-pico.sh-ssh-key>`)

For instance, use this to encode your `id_rsa` key.

```sh
base64 -w0 < $HOME/.ssh/id_rsa
```

WARNING: your key cannot be protected by password.

### Deploy the controller

The following must be run as an administrator:

```sh
kubectl apply -f k8s/combined.yaml
```

Verify that everything is working:

```sh
❯ kubectl get pods -o wide -n pico-system
NAME                                       READY   STATUS    RESTARTS   AGE   IP             NODE     NOMINATED NODE   READINESS GATES
pico-ingress-controller-7cc6f8b5dd-9cr6h   1/1     Running   0          16m   10.50.228.71   k8s-w1   <none>           <none>
```

### Run a demo application

Run the following as any user, in any namespace where you have permission to create deployments, services, and ingresses.

```sh
kubectl apply -f example-app/combined.yaml
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

### Clean up

Demo app:

```sh
kubectl delete -f example-app/combined.yaml
```

Controller:

```sh
kubectl delete -f k8s/combined.yaml
```

## Future improvements

- Replace the controller bash script with a go application
- This includes subscribing to Ingress changes instead of polling for IngressClass usage

## License

Copyright 2025 Davide Guerri

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.

NOTICE: This project was created by Davide Guerri. Modifications must retain attribution.