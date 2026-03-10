# Debug Design (Simplified)

This document defines the target architecture for `krun debug` with focus on the core
goal: run the app locally, set a breakpoint, and hit that breakpoint from cluster traffic.

## Goals

1. Cluster traffic to a target service is executed by a local process.
2. The local process can still access cluster dependencies through localhost.
3. Keep the implementation simple enough to operate and debug.

## Design Principles

1. Keep `krun-helper` as the only dev-machine component that talks to Kubernetes.
2. Keep `traffic-manager` as control-plane plus relay in the cluster.
3. Use one manager port (`:8080`) for REST and streaming.
4. Do not use custom binary wire framing.
5. Do not port-forward the target service to local intercept port.
   The intercept path must go through agent -> manager -> helper -> local app.

## Component Map

```
Developer machine                          Kubernetes cluster
----------------------------------         ---------------------------------
krun (CLI)                                 krun-system namespace
  -> krun-helper (daemon)                    -> traffic-manager (controller+relay :8080)
       - /etc/hosts management
       - dependency port-forward registry   target workload namespace
       - manager API proxy + stream client    -> traffic-agent (injected sidecar)
       - local connection bridge                    - iptables REDIRECT
                                                    - intercepted TCP listener
```

## Traffic Flow (Breakpoint Path)

1. Caller pod sends TCP traffic to target service as usual.
2. In the target pod, `traffic-agent` redirects target port to its local listen port via iptables.
3. `traffic-agent` sends that intercepted connection over its session stream to `traffic-manager`.
4. `krun-helper` maintains a stream attachment for the same session (via manager port-forward).
5. For each intercepted connection, helper opens `127.0.0.1:<intercept_port>` and proxies bytes.
6. Local app handles request and returns bytes back through helper -> manager -> agent -> caller.

## What Changes vs Legacy Design

1. Remove dedicated tunnel port `:8090`; manager serves both REST and stream on `:8080`.
2. Remove custom `TunnelFrame` binary protocol and manual byte parsing.
3. Keep logical `connection_id` multiplexing, but transport it via standard stream messages.
4. Remove helper target-service port-forward (`container_port -> intercept_port`).
5. Keep helper dependency port-forwards and hosts entries.

## Session Lifecycle

### Enable

1. CLI ensures helper is running.
2. Helper writes hosts entries and starts dependency port-forwards from `service_dependencies`.
3. Helper creates debug session through manager REST (`POST /v1/sessions`).
4. Manager injects `traffic-agent` sidecar into the target workload.
5. Helper starts/maintains stream attachment for the session and validates local intercept port.
6. Helper marks session active.

### Disable

1. Helper stops the session stream attachment.
2. Helper deletes debug session through manager REST (`DELETE /v1/sessions/{id}`).
3. Manager removes sidecar and rolls workload.
4. Helper removes dependency port-forwards and hosts entries.

### List

1. CLI asks helper for local view.
2. Helper can enrich output with manager session state if connected.

## Manager API

REST (HTTP on `:8080`):

1. `POST /v1/sessions`
2. `GET /v1/sessions`
3. `DELETE /v1/sessions/{id}`

Streaming (same port, upgraded protocol):

1. Helper attaches as session client.
2. Agent attaches as session agent.
3. Messages are typed envelopes (`open`, `data`, `close`, `error`, `ping`).

Implementation detail: gRPC bidirectional stream is preferred for typed envelopes and
backpressure handling without custom framing code.

## traffic-agent Responsibilities

1. Configure and clean up iptables redirect rule:
   `target_port -> agent_listen_port`.
2. Accept intercepted TCP connections.
3. For each connection:
   - create connection id
   - emit `open`
   - stream `data`
   - emit `close`/`error`
4. Reconnect stream with bounded backoff.

Security/runtime requirements remain:

1. `NET_ADMIN` capability
2. root user in agent container
3. `iptables` available in image

## krun-helper Responsibilities

1. Own hosts-file lifecycle for dependencies.
2. Own dependency port-forward lifecycle.
3. Maintain manager API port-forward.
4. Maintain session stream attachment(s).
5. Bridge each incoming tunneled connection to `127.0.0.1:<intercept_port>`.
6. Clean up all resources on shutdown.

## Failure Handling

1. No helper stream attached:
   - manager rejects/queues new agent `open` (reject in v1 for simplicity).
2. Local app not listening on intercept port:
   - helper returns per-connection error and closes connection.
3. Manager restart:
   - session state is lost; helper reports inactive sessions; user re-enables debug.
4. Helper restart:
   - helper rebuilds local state only from explicit user action (`disable`/`enable`).

## Runtime Manifests (Target State)

1. `krun-traffic-manager` service exposes only `8080`.
2. Sidecar env points to manager address on `:8080`.
3. No separate tunnel service port.

## Implementation Phasing

1. `Phase 1`: implement manager REST + sidecar injection/removal.
2. `Phase 2`: implement stream attach and helper bridge using standard typed messages.
3. `Phase 3`: enable full `krun debug enable/disable/list` orchestration.
4. `Phase 4`: add resilience and diagnostics.
