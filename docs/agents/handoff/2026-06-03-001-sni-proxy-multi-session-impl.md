# Handoff — SNI proxying + multi-session SSH architecture

**Date:** 2026-06-03
**Branch:** `feat/sni-proxy-multi-session` (3 commits ahead of `main`)
**Spec:** `docs/superpowers/specs/2026-06-03-sni-proxying-design.md`
**Next phase:** implementation (this session ended after spec approval)

## What was accomplished

- Brainstormed and **wrote the design spec** for two interlocking changes:
  1. Adding sish SNI passthrough (`sni-proxy=true`) via a new `TLSRoute` reconciler.
  2. Refactoring the SSH side of the controller to a **pool of up to three SSH sessions** (plain, proxy-protocol, sni-proxy) inside a *single* controller deployment, instead of today's "one deployment per session flavour" pattern.
- Created branch `feat/sni-proxy-multi-session` (off `main`).
- Committed the spec in three steps:
  - `8f22ca4` — initial spec.
  - `f072dad` — Documentation-updates section (README, CONFIGURATION, CHANGELOG, ARCHITECTURE, etc.).
  - `82689ec` — switched session-selection from route-annotation (Option A) to per-listener Gateway annotation + `parentRefs[].sectionName` (Option B).
- No code changes yet. No tests touched. No deployment YAMLs added.

## Key decisions

### Architecture: Approach 1 (pool of `SSHTunnelManager`s)
- New `SSHSessionPool` holds `map[SessionKind]*SSHTunnelManager`. `SSHTunnelManager` itself is mostly unchanged (well-tested, ~1000 LOC).
- Rejected Approach 2 (multi-session inside one manager) — too much rework of the most-tested file.
- Rejected Approach 3 (third dedicated deployment for SNI, status-quo pattern) — doesn't deliver the consolidation the user wants.

### Session selection: Option B (per-listener Gateway annotation)
- Session-flag selection lives on the **listener**, not the route. New annotation:
  ```
  ssh-gateway.io/listener-proxy-protocol.<listenerName>: "true"
  ```
  set on the `Gateway`, keyed by the listener's `spec.listeners[].name`.
- TCPRoutes pick the flavour they want via `parentRefs[].sectionName` pointing at a plain or PP-bound TCP listener. Routes carry no session-selection metadata.
- `Listener.sessionKind` is set once at registration in `createListener` and is static. If the Gateway annotation changes, the existing forward is stopped on the old session and restarted on the new — same control flow as today's hostname/backend change.
- `GatewayReconciler.SetRoute` signature stays unchanged. Route reconcilers don't compute `SessionKind`.

### GatewayClass annotations (gate which sessions the pool opens)
- `ssh-gateway.io/proxy-protocol: "1"|"2"` — existing annotation, semantics unchanged; opens the PP session at version N.
- `ssh-gateway.io/sni-proxy: "true"` — new; opens the SNI session.
- Both may coexist on one class. Neither = today's plain-only behaviour.

### Listener compatibility (and rejections)
- `HTTP` → `HTTPRoute` → plain session.
- `TCP` (no PP listener annotation) → `TCPRoute` → plain session.
- `TCP` (PP listener annotation) → `TCPRoute` → pp session. Requires class `proxy-protocol` annotation.
- `TLS` with `tls.mode: Passthrough` → `TLSRoute` → sni session. Requires class `sni-proxy: "true"`.
- `TLS` with `mode: Terminate` → rejected (`UnsupportedTLSMode`); the controller never holds backend certs.
- `HTTPS` → rejected (`UnsupportedListenerProtocol`); same reason.

### Status conditions (placement matters)
- Misconfiguration surfaces on the **listener** (`Programmed=False`), with reasons:
  - `SessionNotEnabled` — listener wants PP/SNI but class hasn't enabled it.
  - `UnsupportedTLSMode` — `TLS` listener using Terminate.
  - `UnsupportedListenerProtocol` — e.g., `HTTPS`.
- Routes inherit the standard Gateway-API `Accepted=False / ListenerNotProgrammed`. **No new route-level reasons.**
- Gateway-level `Programmed=True` iff every session **currently in use by ≥1 listener** is connected; sessions enabled but unused don't block it.

### Logging discipline (user-requested)
- Every log line from `SSHTunnelManager` and `SSHSessionPool` MUST carry a `session=<label>` field. Bake it in via `slog.With("session", label)` at construction; don't thread the label through call sites.
- Labels: `plain`, `pp:v1`, `pp:v2`, `sni`.

### Test coverage (saved as memory)
- Baseline at design time: `controllers` 93.0%, `ssh` 78.1%. **Must not regress.** Improve where practical. Memory: `feedback-test-coverage` in the per-project memory dir.

### Deployment strategy
- **Existing deployments stay untouched.** User explicitly said: "leave current deployments up. we will create a new class for the new controller and test it."
- New deployment file: `k8s/combined-with-data-pico-multi-session.yaml` with controllerName `pico.sh/ssh-gateway-api-controller-multi` and GatewayClass `pico-sh-multi-cl` (annotated with both new flags). Operators can drop either annotation to disable that session.
- New example: `k8s/example-sni.yaml` — Gateway with two TCP listeners (plain + PP) and a TLS Passthrough listener, plus a TLSRoute.

### Things explicitly ruled out
- Multi-route-per-TCP/TLS-listener semantics beyond today's "last writer wins". Today `Listener.route` is a singular pointer; that's intentional and correct for L4 (TCPRoute / TLSRoute have no demux). HTTPRoute is unchanged.
- Custom `SSHForwardPolicy` CRD (Option C in the brainstorm). Overkill for one boolean.
- Cert-manager integration in the controller. Cert handling is the backend pod's job — guidance only in docs.
- Gateway-API-spec change of `SetRoute` signature.

## Important context for future sessions

### Picking up the implementation
- The spec at `docs/superpowers/specs/2026-06-03-sni-proxying-design.md` is the source of truth. Re-read it before touching code.
- Branch `feat/sni-proxy-multi-session` is checked out. Three doc commits are present; no code commits.
- Next step per the brainstorming flow is to **invoke the `superpowers:writing-plans` skill** to turn the spec into a step-by-step implementation plan, then implement.

### Code touch-points
- `controllers/common.go` — adds `parseClassSessionConfig`, `SessionKind` enum, `createListener` learns to derive `sessionKind` from Gateway annotations + protocol.
- `controllers/gateway_controller.go` — `manager` field becomes a `SSHSessionPool`. Adds `ConfigureSessions(...)` calls. `Listener` struct gains `sessionKind`. `SetRoute` internals route by `l.sessionKind`. Multi-session `Programmed` aggregation.
- `controllers/tlsroute_controller.go` (new) — mirror of `tcproute_controller.go`, watches `gatewayv1alpha2.TLSRoute`, validates `tls.mode: Passthrough`.
- `controllers/scheme.go` — verify TLSRoute is registered by `gatewayv1alpha2.AddToScheme` (almost certainly already is).
- `ssh/ssh_tunnel_manager.go` — accept `SessionFlags{ProxyProtocolVersion, SNIProxy}` at construction; remove `SetProxyProtocol`/`GetProxyProtocol`; bake `sessionLabel` into slog context.
- `ssh/ssh_session_pool.go` (new, or could live in `controllers/`) — the pool.
- `main.go` — register the new `TLSRouteReconciler`.
- `k8s/shared.yaml` — RBAC for `tlsroutes`, `tlsroutes/status`, `tlsroutes/finalizers`.
- `k8s/combined-with-data-pico-multi-session.yaml` and `k8s/example-sni.yaml` — new files.

### Existing PP pattern to mirror
- See `controllers/common.go:165-180` (`parseProxyProtocol`) and `ssh/ssh_tunnel_manager.go:104,374-399,990-1005` for how `proxy-protocol=N` is sent as an SSH exec command. SNI is the same shape (`sni-proxy=true`).
- See `k8s/combined-with-data-pico-proxy-protocol.yaml` and `k8s/example-proxy-protocol.yaml` for the YAML pattern of the additional deployment.

### Docs to update (do not skip)
The spec lists these explicitly under "Documentation updates":
- `README.md`, `CONFIGURATION.md`, `CHANGELOG.md`, `ARCHITECTURE.md`
- `docs/ssh-providers.md`, `docs/examples.md`
- `docs/DIAGRAMS.md` + `.dot` source + regenerated `.png`/`.svg`

### Coverage gate
- Run `go test ./... -cover` before and after. Don't regress controllers 93.0% / ssh 78.1%. The per-project memory file `feedback_test_coverage.md` captures this as a hard rule.

### Known pre-existing state (no work needed)
- `main` is clean. CI / pre-commit hook runs `fmt`, `vet`, `lint`, `test`.
- Coverage is published to Coveralls (badge in README).
- Linter config: `.golangci.yml`, 33+ linters enabled.
- `gatewayv1alpha2` is already vendored (TCPRoute uses it; TLSRoute lives in the same package).

### Gotchas to watch
- **sish session flags are session-level, not per-forward.** This is the entire reason for the pool. Do not try to set `proxy-protocol=2` on a single `-R` forward — it doesn't work that way.
- The pool's plain manager is constructed eagerly at controller start; PP and SNI are **lazy** — opened only when `ConfigureSessions` flips them on. When toggled off, **drain forwardings first**, then `Stop()`.
- `ConfigureSessions` is the **only writer** of the session set; serialise pool mutations behind its `RWMutex`. `StartForwarding`/`StopForwarding` are concurrent readers + per-manager writers.
- Removing `SetProxyProtocol` from `SSHTunnelManagerInterface` is fine — only the `controllers` package consumes it. Update tests accordingly.

### User preferences captured in this session
- Test coverage must never regress (memory: `feedback-test-coverage`).
- Existing deployments must not be touched in this PR.
- Prefer landing the refactor and SNI together on this single feature branch (no need to split).
- Per-session SSH log labels are mandatory.
