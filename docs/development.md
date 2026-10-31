# Development

## Building

Build the CLI binaries for Windows and Linux:

```sh
make build-all VERSION=<version> GOARCH=amd64
```

Individual targets:

```sh
make build-linux            # krun + krun-helper for Linux
make build-windows          # krun.exe + krun-helper.exe
```

`VERSION` is stamped into `main.version`, which also selects where
`krun debug runtime install` fetches its manifests from: the default `debug`
renders `deploy/runtime/overlays/local` locally, any other value downloads the
release bundle for that tag. Override the source with `KRUN_MANIFEST_URL`.

## Windows helper build

`krun-helper.exe` carries an embedded `asInvoker` manifest, so it runs as
whoever launches it. krun starts the helper daemon as the current user, and
starts `krun-helper hosts-daemon` (the privileged process that writes the
hosts file) through the ShellExecute `runas` verb, which shows the UAC prompt
([elevate_windows.go](../internal/krun/debug/elevate_windows.go)). Do not switch
the manifest to `requireAdministrator`: Windows then refuses the plain
unelevated daemon launch with `ERROR_ELEVATION_REQUIRED`.

The manifest lives in
[cmd/krun-helper/resource_windows_amd64.syso](../cmd/krun-helper/resource_windows_amd64.syso),
which the Go linker embeds automatically for `GOOS=windows GOARCH=amd64` — so
`make build-windows` needs no post-link step and cross-compiles from Linux.

Regenerate the `.syso` only if `scripts/krun-helper.manifest` changes:

```sh
go run github.com/akavel/rsrc@v0.10.2 \
  -manifest scripts/krun-helper.manifest -arch amd64 \
  -o cmd/krun-helper/resource_windows_amd64.syso
```

Verify any built binary with `python3 scripts/verify-uac-manifest.py krun-helper.exe`,
which checks for an `RT_MANIFEST` resource requesting `asInvoker`. CI runs the
same check.

Note the `.syso` is `amd64`-specific by filename; a Windows build for another
architecture would need its own generated alongside it.

## Inspecting a running helper

The helper serves HTTP+JSON over a local IPC endpoint. `krun debug helper
status` is the supported read-only view — it never starts the helper. To poke
at the API directly on Linux:

```sh
curl --unix-socket "$XDG_RUNTIME_DIR/krun/krun-helper.sock" http://localhost/v1/debug/sessions
```

Show helper and port-forward processes:

```sh
# Linux
sudo ps -ef | grep krun-helper | grep -v grep
```

```powershell
# Windows
Get-CimInstance Win32_Process |
  Where-Object { $_.Name -like 'krun-helper*' } |
  Select-Object ProcessId, Name, ParentProcessId, CommandLine
```

## Testing

```sh
go test ./...
go test ./... -race     # the stream bridge is concurrent; run this before releasing
```

The tests exercise local behavior with fakes, sockets, and temporary files.
Changes to the port-forward registry, `internal/streamlink`, or the manager
transport also need verification against a real cluster; see the traffic flow
in [debug.md](debug.md).

## Layout

| Path | What it is |
| --- | --- |
| `cmd/krun` | the CLI |
| `cmd/krun-helper` | unprivileged dev-machine daemon, plus the small privileged `hosts-daemon` |
| `cmd/traffic-manager` | in-cluster control plane and stream relay |
| `cmd/traffic-agent` | injected sidecar that intercepts traffic |
| `internal/streamlink` | the session stream protocol, shared by agent and helper |
| `internal/krun-helper/session` | one `Session` per active debug session |
| `internal/traffic-manager/agent` | sidecar injection and probe rewriting |
| `deploy/runtime` | kustomize bases and overlays for the in-cluster runtime |
