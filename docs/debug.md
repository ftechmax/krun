# Debugging architecture

Cluster traffic reaches a local app through the traffic-agent, traffic-manager,
and krun-helper. Dependency port-forwards let the local app call cluster services.

## Component Map

```
Developer machine                          Kubernetes cluster
----------------------------------         ---------------------------------
krun (CLI)                                 krun-system namespace
  -> krun-helper (daemon)                    -> traffic-manager (controller+relay :8080)
       - desired hosts entries
       - dependency port-forward registry   target workload namespace
       - manager port-forward (REST + WS)     -> traffic-agent (injected sidecar)
       - local connection bridge                    - iptables REDIRECT
                                                    - intercepted TCP listener
                                                    - probe server (:8082)
```

The CLI starts the privileged hosts writer when needed. The helper sends
combined hosts entries directly to that writer while holding its session
operation lock, keeping hosts writes in the same order as session changes.

## CLI <-> Helper Transport

The CLI talks HTTP+JSON to the helper over a local IPC endpoint, not TCP:

1. Linux: unix domain socket `$XDG_RUNTIME_DIR/krun/krun-helper.sock`, mode
   `0600`. The helper runs as the invoking user, so the socket needs no
   chown and the endpoint is per-user rather than one-per-machine. Where
   `XDG_RUNTIME_DIR` is unset, a uid-scoped directory under the temp dir is
   used instead and is checked to be a real `0700` directory owned by the
   current user.
2. Windows: named pipe `\\.\pipe\krun-helper` with the default security
   descriptor — helper and CLI run as the same user.
3. Override with `krun-helper --socket <endpoint>`.

Poking around: `krun debug helper status` (read-only, never auto-starts the
helper) or, on Linux,
`curl --unix-socket "$XDG_RUNTIME_DIR/krun/krun-helper.sock" http://localhost/v1/debug/sessions`.

Enable and disable send the discovered `config.Service` directly as JSON.
The helper keys sessions by `project/name`; namespace and application port
defaults and dependency DNS targets are resolved during discovery. The CLI, helper, manager, and agent share
an internal protocol and should be rebuilt and upgraded together. Stop a running helper with
`krun debug helper stop` before replacing its binary.

## Traffic Flow (Breakpoint Path)

1. Caller pod sends TCP traffic to target service as usual.
2. In the target pod, `traffic-agent` redirects target port to its local listen port via iptables.
3. `traffic-agent` sends that intercepted connection over its session stream to `traffic-manager`.
4. `krun-helper` maintains a stream attachment for the same session (via manager port-forward).
5. For each intercepted connection, helper opens `127.0.0.1:<intercept_port>` and proxies bytes.
6. Local app handles request and returns bytes back through helper -> manager -> agent -> caller.

## Session Lifecycle

### Enable

1. CLI starts the privileged hosts writer and unprivileged helper if needed.
2. Helper starts dependency port-forwards from `service_dependencies`.
3. Helper creates a manager session (`POST /v1/sessions`); the manager injects
   the traffic-agent sidecar into the target workload.
4. Helper starts the session stream and records the session as active. Local
   app connections are opened when intercepted traffic arrives.
5. Helper applies the combined hosts entries through the privileged writer.
   CLI exports the running container's environment to `.env`.

For `.env` export, `--container <name>` overrides the service's optional
`container` field in `krun.json`. Without either, the CLI selects a container
only when exactly one remains after excluding the injected traffic-agent.
Multiple candidates produce an error listing their names; service-name and
port matches do not resolve ambiguity. An export failure is reported as a
warning after session creation; the debug session remains active.

On re-enable, the helper deletes the previous manager session before replacing
its stream. If deletion fails, the existing local session stays intact.
Dependency forwards remain available for reuse. Later failures roll back completed
steps; a failed manager-session deletion leaves a `cleanup-pending` session
for `disable` to retry. Local forward cleanup has no error return.

A hosts-write failure is returned to the CLI without undoing the session
change. Re-enable or disable retries the hosts update. Helper shutdown also
clears the hosts block.

Dependency forwards require a running pod and a port exposed by the Kubernetes
Service. Numeric and named target ports are supported; an unmatched service
port is an error rather than being treated as a pod port.

### Disable

1. Helper stops the session stream and deletes the manager session.
2. Manager removes the sidecar and restores the original probes.
3. Helper removes dependency forwards; failed teardown remains retryable.
4. Helper applies the remaining sessions' hosts entries. CLI removes the
   service's `.env` file after successful teardown.

### List

1. CLI asks helper for its local view of active sessions.

## Manager API

REST (HTTP on `:8080`):

1. `POST /v1/sessions`
2. `GET /v1/sessions`
3. `DELETE /v1/sessions/{id}`

The helper reaches these over the same port-forward that carries the traffic
streams, so there is one transport to the manager and one failure mode to
diagnose.

Session creation sends only the namespace, service name, and container port.
The manager uses the service name to find the workload; the local intercept port
and dependency settings stay in the helper.

Session CRUD requires the bearer token in `~/.krun/manager-token`, sent in the
`X-Krun-Auth-Token` header. `krun debug runtime install` creates the token with
private permissions when absent and reuses it on upgrades. Only its SHA-256
verifier is injected into the traffic-manager Deployment; neither the helper
nor the manager reads Kubernetes Secrets. Network reach to the Service alone
is therefore not enough to create sessions (which inject root+`NET_ADMIN`
sidecars). `/healthz` and the stream endpoints are exempt; streams authenticate
once during websocket attachment with their per-session token in
`X-Krun-Session-Token`. Tokens are never put in URLs or data envelopes.

The local manager token lifecycle is explicit:

1. Fresh install creates `~/.krun/manager-token` (`0700` directory and `0600`
   file on Unix; user-only ACL on Windows).
2. Upgrade reuses it, so a running helper remains valid.
3. Uninstall retains it for a later reinstall.
4. To rotate deliberately, stop the helper, remove the local token file, and
   run `krun debug runtime install` again. Restarting the helper after the
   install makes it read the newly generated token. Do not delete the token
   while a helper is running.
5. A missing, malformed, or broadly readable token fails locally with an
   instruction to reinstall the runtime; the credential is never recovered
   from the cluster.

Streaming (same port, upgraded protocol):

1. Helper attaches as session client.
2. Agent attaches as session agent.
3. Messages are typed envelopes (`open`, `data`, `close-write`, `close`,
   `error`), keyed by connection ID. The authenticated websocket identifies
   the session, so envelopes do not repeat it.
4. `close-write` half-closes one direction (sender saw EOF on its read
   side); the connection is torn down on `close`/`error` or once both
   directions are closed. Protocols that half-close and then await the
   response keep working.
5. Liveness uses websocket ping/pong control frames: every peer pings on a
   30s cadence and reads carry a 60s deadline, so half-open connections
   (e.g. through a dropped port-forward) are detected and re-dialed instead
   of silently eating traffic.
6. Every peer runs its reads and writes on separate goroutines
   (`streamlink.Serve`). A receive handler routinely answers an envelope by
   sending one back, so a single combined pump would deadlock against its own
   full outbound queue.

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
   `krun.ftechmax.net/original-probes` annotation and restored on removal).
   The pod stays Ready while the developer's local app is stopped or paused
   on a breakpoint.

Security/runtime requirements remain:

1. `NET_ADMIN` capability
2. root user in agent container
3. `iptables` available in image

## Privilege model

krun runs two local processes, split so that almost nothing runs as root:

1. `krun-helper` (unprivileged) — the kubeconfig, the manager bearer token,
   the dependency port-forwards, the manager port-forward and the traffic
   stream. All of it as the invoking user.
2. `krun-helper hosts-daemon` (privileged) — the only root process. It accepts
   a list of hosts entries on a restricted local socket and rewrites krun's
   block in the machine's hosts file. It holds no credentials, opens no
   outbound connections, and knows nothing about debug sessions.

The hosts writer is long-lived on purpose. It starts on the first
`krun debug enable` that needs it, so the developer answers **one** elevation
prompt and then enables and disables sessions for the rest of the day without
being asked again. Elevation is requested by the `krun` CLI, never by the
helper daemon: a detached daemon has no controlling terminal, so `sudo` would
refuse to prompt it.

On Linux, `sudo` must be on `PATH` and allow the user to run the hosts writer
as root. The CLI runs `sudo -b -- krun-helper hosts-daemon`: sudo authenticates
in the terminal and starts the writer in the background. Run the CLI as your
regular user so the main helper and cluster credentials stay unprivileged.
Windows uses the UAC `runas` prompt.

Because the writer takes requests off a socket while running as root, it
treats them as untrusted:

1. The socket is mode `0600`, chowned to the user who authorised the
   elevation (`SUDO_UID`); on Windows the pipe DACL admits the
   interactive user alongside SYSTEM/Administrators.
2. Addresses must be loopback. krun only ever points dependency names at
   `127.0.0.1`, so refusing anything else means a request can at worst
   redirect a name to the local machine, never to a host an attacker
   controls.
3. Hostnames are restricted to letters, digits, `-`, `.` and `_`, which keeps
   a crafted entry from injecting extra lines into the hosts file.
4. Unknown JSON fields and oversized bodies are rejected.

`krun debug helper stop` stops the writer, which finishes in-flight requests
and then strips krun's block as it exits. That is also how a developer
reclaims their hosts file.

## krun-helper Responsibilities

1. Apply the combined dependency hosts entries through the privileged writer.
2. Own dependency port-forward lifecycle.
3. Maintain the manager port-forward that carries both REST and streams.
4. Maintain session stream attachment(s).
5. Bridge each incoming tunneled connection to `127.0.0.1:<intercept_port>`.
6. Clean up all local resources on shutdown. Manager-side sessions and
   sidecars remain, but the helper does not persist enough local state to keep
   traffic flowing across a restart. Re-enabling replaces the manager session;
   manager startup also sweeps orphaned sidecars.

`session.Session` holds the service context, manager session ID, state, and
stream. The port-forward registry tracks shared dependency forwards by session
key; hosts entries are derived from the stored service contexts.

## Failure Handling

1. No helper stream attached:
   - manager rejects new agent `open` messages.
2. Local app not listening on intercept port:
   - helper returns per-connection error and closes connection.
3. Manager restart:
   - manager session state is lost. Re-enable debug to create a new session;
     the helper's local list does not verify manager state.
4. Helper restart:
   - local state is lost and traffic stops; re-enable to replace the retained
     manager session and sidecar.

## Runtime Manifests

1. `krun-traffic-manager` service exposes only `8080`.
2. Sidecar env points to manager address on `:8080`.
3. No separate tunnel service port.
4. The manager receives only the one-way verifier for the locally stored
   bearer token. Its service account has no Kubernetes Secret permissions.

## Notes

1. The helper's manager-API port-forward requests local port 0 (OS-picked),
   so a busy port can never block helper startup.
2. Per-connection TCP writes carry a 10s deadline on both bridge ends. Writes
   are currently dispatched synchronously, so one blocked connection can delay
   the session pump for up to that deadline before being closed with an error.
3. The envelope protocol and the reconnecting websocket peer live in
   `internal/streamlink`, shared by the agent (cluster side) and the helper
   (dev-machine side) so the half-close rules have one definition.
