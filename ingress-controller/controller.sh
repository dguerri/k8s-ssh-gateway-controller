#!/bin/bash
# Copyright 2025 Davide Guerri
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
#
# NOTICE: This project was created by Davide Guerri. Modifications must retain attribution.

set -euo pipefail

# Configuration: customizable through environment variables
INGRESS_CLASS=${INGRESS_CLASS:-pico-sh-tunnel}
SSH_USER=${SSH_USER:-tunnel}
SSH_HOST=${SSH_HOST:-tuns.sh}
SSH_KEY_PATH=${SSH_KEY_PATH:-/ssh/id}
SLEEP_TIME=${SLEEP_TIME:-15}

echo "Starting Pico.sh Ingress controller"

# Main loop: runs indefinitely to manage tunnels based on Ingress resources
while true; do
  # Fetch all Ingress resources and extract active hosts with the specified IngressClass
  ingresses=$(kubectl get ingress -A -o json)

  mapfile -t active_hosts < <(echo "$ingresses" | jq -r \
    '.items[] | select(.spec.ingressClassName=="'"$INGRESS_CLASS"'") | .spec.rules[0].host')

  if [ "${#active_hosts[@]}" -gt 0 ]; then
    echo "Active host(s): ${active_hosts[*]}"
  fi

  # Ensure autossh tunnels are running for all active Ingress hosts
  for host in "${active_hosts[@]}"; do
    ingress=$(echo "$ingresses" | jq -c '.items[] | select(.spec.rules[0].host=="'"$host"'")')
    name=$(echo "$ingress" | jq -r '.metadata.name')
    service=$(echo "$ingress" | jq -r '.spec.rules[0].http.paths[0].backend.service.name')
    port=$(echo "$ingress" | jq -r '.spec.rules[0].http.paths[0].backend.service.port.number')
    namespace=$(echo "$ingress" | jq -r '.metadata.namespace')

    echo "Ensuring tunnel for $name -> $host -> $service:$port"

    if ! pgrep -f "autossh.*$host.*$service.*$port" > /dev/null; then
      echo "Starting autossh tunnel for $name"
      autossh -M 0 -N -f \
        -i "$SSH_KEY_PATH" \
        -o StrictHostKeyChecking=no \
        -R "$host:80:$service.$namespace.svc.cluster.local:$port" \
        "$SSH_USER@$SSH_HOST"
    else
      echo "Autossh tunnel for $name is running" 
    fi
  done

  # Find and terminate autossh tunnels that no longer have a corresponding Ingress
  (pgrep -af "autossh.*$SSH_USER@$SSH_HOST" || true) | while read -r pid cmd; do
    found=0
    for host in "${active_hosts[@]}"; do
      ingress=$(echo "$ingresses" | jq -c '.items[] | select(.spec.rules[0].host=="'"$host"'")')
      service=$(echo "$ingress" | jq -r '.spec.rules[0].http.paths[0].backend.service.name')
      port=$(echo "$ingress" | jq -r '.spec.rules[0].http.paths[0].backend.service.port.number')
      namespace=$(echo "$ingress" | jq -r '.metadata.namespace')
      if echo "$cmd" | grep -q "$host:80:$service.$namespace.svc.cluster.local:$port"; then
        found=1
        break
      fi
    done

    if [ "$found" -eq 0 ]; then
      echo "Killing stale autossh tunnel: $cmd"
      kill "$pid"
    fi
  done

  # Sleep before next reconciliation loop
  sleep "${SLEEP_TIME}"
done
