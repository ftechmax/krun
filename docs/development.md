# Development

## Commands

Build release CLI zips (same commands used in workflows):
```sh
make release-binaries VERSION=<version> GOARCH=amd64
```

Build helper binaries for both Windows and Linux:
```sh
make build-helper-cross GOARCH=amd64
```

Show krun-helper processes
```sh
sudo ps -ef | grep 'kubectl.*port-forward' | grep -v grep
```

Show helper port forward processes
```sh
sudo ps -ef | grep 'kubectl.*port-forward' | grep -v grep
```

Get-CimInstance Win32_Process | Where-Object {$_.ProcessId -in 47556,52872,91172} | Select ProcessId,Name,ParentProcessId,CommandLine
Get-CimInstance Win32_Process | Where-Object {$_.ProcessId -in 86284,37528} | Select ProcessId,Name,ParentProcessId,CommandLine

## Windows helper build

Build and patch `krun-helper.exe` with an embedded `requireAdministrator` manifest:

```powershell
make build-helper-windows-uac
```

This invokes `scripts/build-helper-windows.ps1`, which builds `./cmd/krun-helper` and applies `cmd/krun-helper/krun-helper.manifest` with `mt.exe`.
