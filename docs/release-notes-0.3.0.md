## What's New

This release is a full rewrite of the debug system. Telepresence and kubectl are no longer required as `krun` now has its own in-cluster runtime and local helper daemon for traffic interception and port-forwarding.

### Debug system rewrite

- Replaced Telepresence with a custom traffic interception stack.
- Added `krun debug runtime install / uninstall` commands to manage the in-cluster runtime components.
- Added `krun debug helper status` and `krun debug helper stop` to inspect and control the local daemon.

### Build improvements

- Copying over source files to the build pod is now done via a tar stream instead of SFTP, improving performance considerably.
- Improved change detection and file pruning during builds.

### Environment file changes

- The debug env file has been renamed from `appsettings-debug.env` to `.env`, making it a universal format compatible with dotenv libraries in any language.
- Kubernetes-injected service discovery variables are now filtered out so only app relevant configurations remain.
- The manual C# env-loading boilerplate in `Program.cs` is no longer needed. Use a dotenv library (e.g. `DotNetEnv`) instead. 

## Upgrading from previous versions

### 1. Delete your old build pod

The build pod has changed. Delete the existing one so it gets recreated on your next build:

```sh
kubectl delete pod docker-build
```

### 2. Install the in-cluster runtime

The debug system now requires runtime components in your cluster. Install them once:

```sh
krun debug runtime install
```

### 3. Update krun.json

The following fields have been added or are now required for service definitions:

- **`intercept_port`** (optional): The local port your app listens on during debug. Default: `5000`. Must be unique per service. I Highly recommended to set this explicitly for each service to avoid conflicts when debugging multiple services at once.
- **`service_dependencies`** (optional): Cluster services your app needs to reach during debug. Each entry sets up a hosts-file entry and port-forward.
- **`aliases`** on dependencies: Extra hostnames that should also resolve to `127.0.0.1` like pod DNS names.

See the [krun.json](README.md#krunjson) section in the README for the full field reference.

### 4. Update your env file loading

The debug env file has been renamed from `appsettings-debug.env` to `.env`. If you were loading the env file manually in `Program.cs`, replace that code with a dotenv library:

1. Add the `DotNetEnv` NuGet package to your project.
2. Remove the `ConfigureConfiguration` boilerplate that loaded `appsettings-debug.env`.
3. Add `builder.Configuration.AddDotNetEnv(".env", LoadOptions.TraversePath());` after creating your builder.

See the [README](README.md#inject-kubernetes-environment-variables) for full examples in C#, Go, and Node.js.

### 5. Telepresence is no longer needed

You can uninstall Telepresence from your machine and cluster if you were only using it for `krun`.


**Full Changelog**: https://github.com/ftechmax/krun/compare/0.2.5...0.3.0