# Architecture Diagrams

This directory contains comprehensive Graphviz diagrams documenting the SSH Gateway API Controller architecture.

## Diagrams

### 1. Simplified Architecture (`simplified-architecture.dot`) **[START HERE]**

**Purpose**: Clean, easy-to-follow diagram showing main components and primary flows.

**Key Elements**:
- Main components (Gateway Controller, Route Controllers, SSH Manager)
- Primary data flows with clear labels
- Three key scenarios visualized:
  1. Route Creation (5 steps)
  2. SSH Reconnection (5 steps)
  3. Route Deletion (5 steps)
- Minimal arrows, maximum clarity

**Best for**: Getting started, presentations, and understanding the big picture.

**Render**:
```bash
dot -Tpng docs/simplified-architecture.dot -o docs/simplified-architecture.png
dot -Tsvg docs/simplified-architecture.dot -o docs/simplified-architecture.svg
```

### 2. High-Level Architecture (`high-level-architecture.dot`)

**Purpose**: Shows the overall system architecture with component responsibilities and data flows.

**Key Elements**:
- Kubernetes resources (GatewayClass, Gateway, HTTPRoute, TCPRoute)
- Controller layer (Route Controllers, Gateway Controller)
- SSH layer (SSH Tunnel Manager)
- External systems (SSH Server, Backends, Internet)
- Architecture principles and design decisions

**Best for**: Understanding overall system design and component interactions.

**Render**:
```bash
dot -Tpng docs/high-level-architecture.dot -o docs/high-level-architecture.png
dot -Tsvg docs/high-level-architecture.dot -o docs/high-level-architecture.svg
```

### 3. Detailed Architecture (`architecture-diagram.dot`)

**Purpose**: Shows detailed function calls and internal component structure with **color-coded flows**.

**Key Elements**:
- All major functions in each controller
- Internal state management (maps, mutexes)
- Function call flows with color coding:
  - 🟢 Green: Setup/Create flows
  - 🔴 Red: Delete/Teardown flows
  - 🟠 Orange: SSH operations
  - 🟣 Purple: Monitoring/Validation
  - 🔵 Blue: Kubernetes API updates
- SSH protocol operations
- Traffic proxy paths
- Color legend for easy navigation

**Best for**: Understanding implementation details, debugging, and following specific flow types.

**Render**:
```bash
dot -Tpng docs/architecture-diagram.dot -o docs/architecture-diagram.png
dot -Tsvg docs/architecture-diagram.dot -o docs/architecture-diagram.svg
```

### 4. State and Data Flow (`state-and-data-flow.dot`)

**Purpose**: Documents state management, data structures, and critical scenarios.

**Key Elements**:
- State stores in Gateway Controller and SSH Manager
- Three critical scenarios:
  1. Normal route setup
  2. SSH reconnect with wrong hostname
  3. Periodic validation (happy path)
- Critical invariants
- Validation logic
- State machine transitions

**Best for**: Understanding state consistency, recovery flows, and edge cases.

**Render**:
```bash
dot -Tpng docs/state-and-data-flow.dot -o docs/state-and-data-flow.png
dot -Tsvg docs/state-and-data-flow.dot -o docs/state-and-data-flow.svg
```

## Quick Reference

| Diagram | Use When | Complexity |
|---------|----------|------------|
| **Simplified** | First time learning, presentations | ⭐ Simple |
| **High-Level** | Understanding architecture, design reviews | ⭐⭐ Medium |
| **Detailed** | Debugging, implementation details | ⭐⭐⭐ Complex |
| **State Flow** | Understanding state management, edge cases | ⭐⭐⭐ Complex |

## Rendering All Diagrams

To generate PNG and SVG versions of all diagrams:

```bash
cd docs
dot -Tsvg simplified-architecture.dot -o simplified-architecture.svg
dot -Tpng simplified-architecture.dot -o simplified-architecture.png
dot -Tsvg high-level-architecture.dot -o high-level-architecture.svg
dot -Tpng high-level-architecture.dot -o high-level-architecture.png
dot -Tsvg architecture-diagram.dot -o architecture-diagram.svg
dot -Tpng architecture-diagram.dot -o architecture-diagram.png
dot -Tsvg state-and-data-flow.dot -o state-and-data-flow.svg
dot -Tpng state-and-data-flow.dot -o state-and-data-flow.png
echo "✓ All diagrams rendered"
```

## Requirements

- **Graphviz**: Install via package manager
  - macOS: `brew install graphviz`
  - Ubuntu/Debian: `apt-get install graphviz`
  - RHEL/CentOS: `yum install graphviz`

## Viewing Diagrams

### Online Viewers
- Upload `.dot` files to [Graphviz Online](https://dreampuf.github.io/GraphvizOnline/)
- Use [Edotor](https://edotor.net/) for editing and viewing

### VS Code
- Install "Graphviz Preview" extension
- Open `.dot` file and use preview pane

### Command Line
```bash
# View directly (macOS)
dot -Tpng architecture-diagram.dot | open -f -a Preview

# Generate and view (Linux with ImageMagick)
dot -Tpng architecture-diagram.dot -o diagram.png && display diagram.png
```

## Architecture Principles Documented

These diagrams document the following key architectural decisions:

1. **Separation of Concerns**
   - Gateway Controller: SSH connection health
   - Route Controllers: Forwarding lifecycle
   - SSH Manager: SSH primitives

2. **Fail Fast Philosophy**
   - Synchronous validation
   - Immediate error returns
   - Kubernetes handles retry with exponential backoff

3. **Single Source of Truth**
   - SSH Manager's `assignedAddrs` map is authoritative
   - `listener.route` may be stale (validated on every access)

4. **Periodic Reconciliation**
   - Routes: 10s (validate forwardings)
   - Gateway: 30s (check connection)

5. **State Consistency**
   - `forwardings` and `assignedAddrs` always cleaned together
   - Lock hierarchy prevents deadlocks: `clientMu` → `addrNotifMu`

6. **Performance Optimizations**
   - `RWMutex` for concurrent reads (`GetAssignedAddresses`)
   - No blocking I/O under locks

## Related Documentation

- [`ARCHITECTURE.md`](../ARCHITECTURE.md) - Comprehensive architecture guide
- [`plans/2025-11-29-route-forwarding-validation-design.md`](plans/2025-11-29-route-forwarding-validation-design.md) - Design document for forwarding validation

## Maintenance

When updating the codebase, ensure diagrams stay synchronized:

1. **New functions**: Add to detailed diagram with appropriate color coding
2. **State changes**: Update state-and-data-flow diagram invariants
3. **Architecture changes**: Update high-level diagram and principles
4. **New scenarios**: Add to state-and-data-flow with step-by-step flows

The diagrams are the source of truth for architecture understanding and should be kept up-to-date with code changes.
