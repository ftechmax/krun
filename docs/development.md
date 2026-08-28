# Development

## Building

Build the CLI binaries for Windows and Linux:

```sh
make build-all VERSION=<version> GOARCH=amd64
```

Individual targets:

```sh
make build-linux            # krun + krun-helper for Linux
make build-windows          # krun.exe + krun-helper.exe (with the UAC patch)
make build-krun-cross       # krun for Windows and Linux
make build-helper-cross     # krun-helper for Windows and Linux
```

`VERSION` is stamped into `main.version`, which also selects where
`krun debug runtime install` fetches its manifests from: the default `debug`
renders `deploy/runtime/overlays/local` locally, any other value downloads the
release bundle for that tag. Override the source with `KRUN_MANIFEST_URL`.

## Windows helper build

`krun-helper.exe` carries an embedded `requireAdministrator` manifest so it
elevates however it is launched:

```powershell
make patch-helper-windows-uac
```

That builds `./cmd/krun-helper` and applies `scripts/krun-helper.manifest` with
`mt.exe` via `scripts/patch-helper-windows.ps1`. The script finds `mt.exe` in
`scripts/`, on `PATH`, or in the Windows SDK.

Note that krun launches the helper through the ShellExecute `runas` verb
([elevate_windows.go](../internal/krun/debug/elevate_windows.go)), which
requests elevation on its own; the manifest covers the other launch paths.

## Inspecting a running helper

The helper serves HTTP+JSON over a local IPC endpoint. `krun debug helper
status` is the supported read-only view — it never starts the helper. To poke
at the API directly on Linux:

```sh
curl --unix-socket /run/krun/krun-helper.sock http://localhost/v1/debug/sessions
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

Unit tests cover everything except the live intercept path. Changes to the
port-forward registry, `internal/streamlink`, or the manager transport need a
real cluster to verify — see the debug flow in [debug.md](debug.md).

## Layout

| Path | What it is |
| --- | --- |
| `cmd/krun` | the CLI |
| `cmd/krun-helper` | elevated dev-machine daemon |
| `cmd/traffic-manager` | in-cluster control plane and stream relay |
| `cmd/traffic-agent` | injected sidecar that intercepts traffic |
| `internal/streamlink` | the session stream protocol, shared by agent and helper |
| `internal/krun-helper/session` | one `Session` per active debug session |
| `internal/traffic-manager/agent` | sidecar injection and probe rewriting |
| `deploy/runtime` | kustomize bases and overlays for the in-cluster runtime |
