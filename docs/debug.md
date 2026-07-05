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
                                                    - probe server (:8082)
```

## CLI <-> Helper Transport

The CLI talks HTTP+JSON to the helper over a local IPC endpoint, not TCP:

1. Linux: unix domain socket `/run/krun/krun-helper.sock`, chowned to the
   invoking user (`SUDO_UID`/`PKEXEC_UID`) with mode `0600`. Only that user
   (and root) can talk to the elevated helper.
2. Windows: named pipe `\\.\pipe\krun-helper` with a security descriptor
   admitting the interactive user alongside SYSTEM/Administrators.
3. Override with `krun-helper --socket <endpoint>`.

Poking around: `krun debug helper status` (read-only, never auto-starts the
helper) or, on Linux,
`curl --unix-socket /run/krun/krun-helper.sock http://localhost/v1/debug/sessions`.

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

Session CRUD requires the shared token from Secret
`krun-system/krun-manager-auth` in the `X-Krun-Auth-Token` header (a custom
header because the API-server service proxy consumes `Authorization` for its
own authentication). The manager generates the token on first start; the
helper reads the Secret through the user's kubeconfig. Network reach to the
Service alone is not enough to create sessions (= inject root+NET_ADMIN
sidecars). `/healthz` and the stream endpoints are exempt; streams
authenticate with their per-session token.

Streaming (same port, upgraded protocol):

1. Helper attaches as session client.
2. Agent attaches as session agent.
3. Messages are typed envelopes (`open`, `data`, `close-write`, `close`,
   `error`, `ping`).
4. `close-write` half-closes one direction (sender saw EOF on its read
   side); the connection is torn down on `close`/`error` or once both
   directions are closed. Protocols that half-close and then await the
   response keep working.
5. Liveness uses websocket ping/pong control frames: every peer pings on a
   30s cadence and reads carry a 60s deadline, so half-open connections
   (e.g. through a dropped port-forward) are detected and re-dialed instead
   of silently eating traffic.

## traffic-agent Responsibilities

1. Configure and clean up iptables redirect rule:
   `target_port -> agent_listen_port`.
2. Accept intercepted TCP connections.
3. For each connection:
   - create connection id
   - emit `open`
   - stream `data`
   - emit `close-write` on caller EOF, `close`/`error` on teardown
4. Reconnect stream with bounded backoff; on reconnect, close all
   connections from the previous stream epoch (the manager already dropped
   their routing).
5. Serve kubelet probes on the probe port (default `:8082`,
   `KRUN_AGENT_PROBE_PORT`): the injector rewrites workload probes that
   target the intercepted port to this port (originals are preserved in the
   `krun.ftechmax.com/original-probes` annotation and restored on removal).
   The pod stays Ready while the developer's local app is stopped or paused
   on a breakpoint.

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
4. Namespaced Role/RoleBinding in `krun-system` let the manager create and
   read the `krun-manager-auth` Secret.

## Notes

1. The helper's manager-API port-forward requests local port 0 (OS-picked),
   so a busy port can never block helper startup.
2. Per-connection TCP writes carry a 10s deadline on both bridge ends; a
   peer that stops reading costs at most that before its connection is
   closed with an error, instead of stalling the whole session pump.
3. Possible future improvement: per-connection writer goroutines in the
   helper/agent bridges for higher throughput under many concurrent
   connections (correctness does not require it).

## Implementation Phasing

1. `Phase 1`: implement manager REST + sidecar injection/removal.
2. `Phase 2`: implement stream attach and helper bridge using standard typed messages.
3. `Phase 3`: enable full `krun debug enable/disable/list` orchestration.
4. `Phase 4`: add resilience and diagnostics.
