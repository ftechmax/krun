# krun

Build, deploy, and debug Kubernetes services from one CLI.

- **Build** images inside the cluster without a local Docker installation.
- **Deploy** projects from their Kustomize manifests.
- **Debug** services by routing cluster traffic to a local app. Set breakpoints
  in your IDE while the app accesses cluster dependencies through local forwards.

krun works with [msa-templates](https://github.com/ftechmax/msa-templates)
services and other Kubernetes apps.

## Installation

1. Get the latest version of `krun` from the [releases page](https://github.com/ftechmax/krun/releases).

2. Unzip the downloaded file to a directory in your `PATH`, or add the directory to your `PATH`.

3. Place a `krun-config.json` file **next to the `krun` binary** to configure your source directory and Docker registries. See the [krun-config.json](#krun-configjson) section for details.

4. Place a `krun.json` file in the root of your project repository to define your services. See the [krun.json](#krunjson) section for details.

5. Install the runtime components in your cluster

   ```sh
   krun debug runtime install
   ```

   You can also apply the release manifest directly if you prefer:

   ```sh
   kubectl apply -f https://github.com/ftechmax/krun/releases/latest/download/krun-traffic-manager.yaml
   ```

6. Follow the [debugging instructions](#debugging-with-krun-runtime) to set up your project for debugging with the krun runtime.

## Quick Start

Before enabling debug, set `intercept_port` in `krun.json` to the local port your app listens on (for example, `http://localhost:5000` from `launchSettings.json`). Ensure the value is unique per service.

1. Enable debug mode for a service:

```sh
krun debug enable <service>
```

2. List active debug sessions:

```sh
krun debug list
```

3. Check local helper daemon status (without starting it):

```sh
krun debug helper status
```

4. Disable debug mode when finished:

```sh
krun debug disable <service>
```

## Usage

```
krun [global options] <command> [command options] <service>
```

### Global Options

- `--kubeconfig <path>`: Path to kubeconfig file (default: `~/.kube/config`)

### Commands

- `help`  
  Show help message.

- `version`
  Show version information.

- `list`
  List all discovered services and projects. Useful for verifying that `krun` has found your `krun.json` files.

  ```sh
  krun list
  ```

- `build [--force, --flush] <project|service>`
  Build a project or specific service.
  Builds run inside a persistent `docker-build` pod in your cluster that caches layers and workspace files between builds. Use `--force` to force a rebuild even if the source has not changed, and `--flush` to delete the existing build pod before building (clearing any cached layers or copied workspace files).

  ```sh
  krun build awesome-app
  krun build awesome-app-api
  krun build --force awesome-app
  krun build --flush awesome-app
  ```

- `deploy [--overlay <name>] [--use-remote-registry, --no-restart, --create-namespace] <project>`
  Deploy a project using the named overlay (default: `local`). The overlays directory is configured by `overlays_dir` in `krun.json` and defaults to `k8s/overlays/`. Select the target cluster with `--kubeconfig`.
  Use `--use-remote-registry` to deploy using the remote Docker registry defined in `krun-config.json`. If not specified, it will use the local Docker registry. Use `--no-restart` to skip the rollout restart after applying manifests. Use `--create-namespace` to discover and create missing target namespaces; without the flag, deploy does not require cluster-scoped Namespace access.

  ```sh
  krun deploy awesome-app
  krun deploy --use-remote-registry awesome-app
  krun deploy --no-restart awesome-app
  krun deploy --overlay production --use-remote-registry awesome-app
  krun deploy --overlay staging awesome-app
  ```

- `remove [--overlay <name>] <project>` (alias: `delete`)
  Delete a deployed project using the named overlay (default: `local`) from the same configured overlays directory.

  ```sh
  krun remove awesome-app --overlay staging
  ```

- `debug list`  
  List active debug sessions.

  ```sh
  krun debug list
  ```

- `debug enable <service> [--container <container>]`
  Enable debug mode for a service using the in-cluster krun runtime.

  ```sh
  krun debug enable awesome-app-api
  ```

  > NOTE: The `krun-helper` daemon runs unprivileged. A small separate hosts writer does need elevation, so the first `krun debug enable` prompts once (Windows UAC / Linux sudo); it then stays running, and later enable/disable commands do not prompt again.

  Debug mode needs a local port configured for your developer machine. Set `intercept_port` in `krun.json` to the port your app listens on locally. Normally you want this to match the `launchSettings.json` `applicationUrl` (for example, `http://localhost:5000`). Ensure the value is unique per service so multiple debug sessions do not conflict.

  Use the `--container` option to specify which container to debug if your pod has multiple containers.

  ```sh
  krun debug enable awesome-app-api --container awesome-app-api
  ```

- `debug disable <service>`  
  Disable debug mode for a service.

  ```sh
  krun debug disable awesome-app-api
  ```

- `debug helper status`
  Check whether the local `krun-helper` daemon is currently running.

  ```sh
  krun debug helper status
  ```

- `debug helper stop`
  Stop the local `krun-helper` daemon and the privileged hosts writer,
  removing krun's block from your hosts file.

  ```sh
  krun debug helper stop
  ```

- `debug runtime install`  
  Install or upgrade the in-cluster debug runtime resources. The command
  creates or reuses the private local manager token at
  `~/.krun/manager-token`; only its verifier is installed in the cluster.

  ```sh
  krun debug runtime install
  ```

- `debug runtime status`  
  Check if the in-cluster debug runtime is healthy and version-aligned.

  ```sh
  krun debug runtime status
  ```

- `debug runtime uninstall`  
  Remove the in-cluster debug runtime resources. The local manager token is
  retained for a later reinstall.

  ```sh
  krun debug runtime uninstall
  ```

## krun-config.json

The [krun-config.json](https://github.com/ftechmax/krun/blob/main/krun-config.json) file configures how `krun` interacts with your Kubernetes environment, Docker registries, and source code. This file must be placed **next to the `krun` binary**. Below is a description of each field:

### Example

```json
{
  "source": {
    "path": "~/git/",
    "search_depth": 1
  },
  "local_registry": "registry:5000",
  "remote_registry": "docker.io/ftechmax"
}
```

### Field Reference

- **`source`**
  - `path`: Root directory where your project source code is located. `krun` will recursively search this directory for `krun.json` files to discover services.
  - `search_depth`: How many directory levels deep to search for `krun.json` files in `path`.

- **`local_registry`**: Address of your local Docker registry (used for local builds). This can include a path segment (for example `registry:5000/myuser`).

- **`remote_registry`**: Address of the remote Docker registry (used for deploying non-local builds).

> Notes  
> Ensure all paths use forward slashes or are properly escaped for your operating system.

## krun.json

The `krun.json` file is used by `krun` to detect available services in your defined source folder. Place it in the root of your project repository, with service definitions under `services` and an optional `overlays_dir` setting.

The **project name** is derived from the directory that contains the `krun.json` file. For example, if the file is located at `c:/git/awesome-app/krun.json`, the project name will be `awesome-app`. This is the name you use in commands like `krun build awesome-app` and `krun deploy awesome-app`.

### Example

```json
{
  "overlays_dir": "k8s/overlays/",
  "services": [
    {
      "name": "awesome-app-worker",
      "path": "src/worker",
      "dockerfile": "AwesomeApp.Worker",
      "context": "src/",
      "service_dependencies": [
        { "host": "awesome-app-cache", "port": 6379 },
        { "host": "rabbitmq.default.svc", "port": 5672 },
        { "host": "ferretdb.ferretdb-system.svc", "port": 27017 }
      ]
    },
    {
      "name": "awesome-app-api",
      "path": "src/api",
      "dockerfile": "AwesomeApp.Api",
      "context": "src/",
      "service_dependencies": [
        { "host": "awesome-app-cache", "port": 6379 },
        { "host": "rabbitmq.default.svc", "port": 5672 }
      ]
    },
    {
      "name": "awesome-app-web",
      "path": "src/web",
      "dockerfile": ".",
      "context": "src/web"
    }
  ]
}
```

### Field Reference

- **`overlays_dir`** (optional): Directory containing the project's Kustomize overlays. Defaults to `k8s/overlays/`. Relative paths are resolved from the directory containing `krun.json`; absolute paths are also accepted. `--overlay staging` selects the `staging` subdirectory.

- **`services`**: Array of service definitions, using the fields below.

- **`name`**: The name of the service as it will be referenced in commands. Make sure this matches the name in your `deployment.yaml` files.

- **`namespace`** (optional): The Kubernetes namespace the service is deployed to. This is used during debug mode to reach the correct workload.

  > NOTE: The default is `default` if not specified.

- **`path`**: The relative path to the service directory from the root of your project. This is where the Dockerfile and source code for the service are located.

- **`dockerfile`**: The path where the Dockerfile is located relative to the `path` directory. If the Dockerfile is in the service directory, you can use `"."`.

- **`context`**: The build context for the Docker image. This is the directory that will be sent to the Docker daemon during the build process. If you have a shared project between multiple services, you can set this to the parent directory of the service directories as shown in the example.

- **`container_port`** (optional): The port of the container running inside the service pod. This is used for debugging and should match the port exposed by your service container.

  > NOTE: The default is `8080` if not specified.

- **`intercept_port`** (optional): The local port that your app listens on when running a debug session. This should match the port your service uses when run locally and be unique per service to avoid conflicts.

  > NOTE: The default is `5000` if not specified.

- **`service_dependencies`** (optional): Service hostnames your local app must resolve during a debug session. Each dependency also triggers a local port-forward so calls go to the cluster.
  - `host`: DNS name used by the application (for example `rabbitmq.default.svc`).
  - `namespace`: Namespace of the dependency service (optional if `host` includes it).
  - `service`: Service name in Kubernetes (optional if `host` includes it).
  - `port`: Port to forward locally (local and remote ports are the same).

### Host Aliases

Some operators (for example MongoDB) generate connection strings that use pod-specific DNS names instead of the service name. When the hostname in a connection string does not match the `host` of any service dependency, you can use `aliases` to add extra hosts-file entries that also resolve to `127.0.0.1`. Traffic is still forwarded through the same port-forward as the parent dependency.

```json
{
  "host": "mongo.default.svc",
  "port": 27017,
  "aliases": ["mongodb-server-0.mongo.default.svc.cluster.local"]
}
```

In this example both `mongo.default.svc` and `mongodb-server-0.mongo.default.svc.cluster.local` will resolve to `127.0.0.1`, and traffic on port `27017` will be forwarded to the `mongo` service in the cluster.

## Debugging with krun Runtime

To debug a service using the krun runtime, first install the runtime components in your cluster and then enable debug mode for the target service.

### Prerequisites

Ensure your project launches as a console application (which is the default profile).

The `krun-helper` daemon runs as your own user and needs no privileges: it holds the kubeconfig, the port-forwards and the traffic stream.

Only one small process needs elevation — the hosts writer, which points dependency hostnames at localhost by editing your hosts file. It starts on your first `krun debug enable` (a UAC prompt on Windows, a `sudo` prompt on Linux) and then stays running, so the rest of your debugging session never prompts again. `krun debug helper stop` shuts it down and removes krun's block from your hosts file.

Install the runtime components once per cluster:

```sh
krun debug runtime install
```

### Enabling Debug Mode

To enable debug mode for a service, use the following command:

```sh
krun debug enable <service>
```

### Optional: Enable traffic-agent diagnostics

To log iptables rules and redirect counters from the traffic-agent, set an env var on the traffic-manager deployment:

```sh
kubectl -n krun-system set env deployment/krun-traffic-manager KRUN_AGENT_DIAGNOSTICS=true
```

Unset it when you are done:

```sh
kubectl -n krun-system unset env deployment/krun-traffic-manager KRUN_AGENT_DIAGNOSTICS
```

### Disabling Debug Mode

To disable debug mode for a service, use the following command:

```sh
krun debug disable <service>
```

### Inject Kubernetes Environment Variables

When debug mode is enabled, krun creates a `.env` file in the service directory containing the environment variables from the running pod. Kubernetes-injected service discovery variables and system variables are filtered out automatically, leaving only app-relevant configuration.

Your application needs a dotenv library to load the `.env` file at startup. Most languages have one:

#### C# (.NET)

Install the [DotNetEnv](https://www.nuget.org/packages/DotNetEnv) NuGet package:

```sh
dotnet add package DotNetEnv
```

Add the `.env` file to your configuration. `TraversePath()` walks up parent directories to find it, so it works regardless of working directory:

```csharp
var builder = WebApplication.CreateBuilder(args);

builder.Configuration.AddDotNetEnv(".env", LoadOptions.TraversePath());
```

#### Go

Use [godotenv](https://github.com/joho/godotenv):

```go
import "github.com/joho/godotenv"

func main() {
    godotenv.Load("../.env")
    // ...
}
```

#### Node.js

Use [dotenv](https://www.npmjs.com/package/dotenv):

```js
require('dotenv').config({ path: '../.env' })
```

> **Note:** The `.env` file path is relative to your application's working directory. Since krun writes the file to the service directory (the `path` from `krun.json`), you typically need `../.env` when your app runs from a subdirectory.
