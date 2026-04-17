This release moves the remaining `krun` functionality into `krun-helper`, so elevated access is no longer needed for every debug command. Once the helper is installed, debug and workspace operations run through this local service, which also allows for automation against the krun-helper API.

## What's Changed

- Added `krun install`, `krun uninstall`, and `krun status` commands to manage both the local `krun-helper` service and the in-cluster `traffic-manager`.
- Added Windows service and Linux `systemd` support for `krun-helper`, including install/upgrade.
- Moved the debug workflow behind the helper service so users do not need to elevate for every debug command anymore.
- Moved `list`, `build`, `deploy`, and `delete` through the helper so workspace operations now run through the local helper service and stream output through a single API surface.
- Added local helper endpoints for service discovery, workspace operations, and debug session management, making `krun-helper` usable via automation as well as a CLI dependency.
- Added version-aware runtime installs that fetch the release manifest matching the CLI version.
- Improved config path handling for elevated runs by expanding `~` and environment variables from the original user context.
- Service discovery now skips invalid `krun.json` files with a warning instead of failing the entire scan.

## Upgrading from 0.3.0

The `krun` CLI now requires the krun-helper background service.  
Install it once:

```sh
krun install
```

**Full Changelog**: https://github.com/ftechmax/krun/compare/0.3.0...0.4.0
