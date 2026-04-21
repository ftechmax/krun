# krun

A command-line tool for building, deploying, and debugging microservices in Kubernetes environments.

## Why krun?

Working with microservices on Kubernetes usually means somehow handling Docker builds, kubectl commands, and other local setups just to test a change. krun wraps all of that into a single CLI:

- **Build** Docker images inside your cluster so no local Docker install is required.
- **Deploy** projects with one command.
- **Debug** a live service by intercepting its cluster traffic and routing it to your local machine, so you can set breakpoints against real requests without mocking anything.

The debug workflow is where krun will really make a difference: enable debug mode, launch your app locally, and traffic that would normally hit the pod flows to your local app instead. Your app can call other cluster services as normal, and you can set breakpoints and inspect variables in your local IDE while handling real requests from the cluster.

This tool pairs perfectly with services made with my [msa-templates](https://github.com/ftechmax/msa-templates) project, but it can be used with any Kubernetes-deployed app.

## Features

- Build Docker images for services
- Deploy services to Kubernetes
- Enable/disable debug mode for services using the in-cluster krun runtime

## Installation

1. Get the latest version of `krun` from the [releases page](https://github.com/ftechmax/krun/releases).

2. Unzip the downloaded file to a directory in your `PATH`, or add the directory to your `PATH`.

3. Configure your source directory and Docker registries in `$HOME/.krun/config.json` on Linux, or `%USERPROFILE%\.krun\config.json` on Windows. The install scripts create a default file if one does not exist. See the [config.json](#configjson) section for details.

4. Place a `krun.json` file in the root of your project repository to define your services. See the [krun.json](#krunjson) section for details.

5. Install the helper service and in-cluster runtime

   ```sh
   krun install
   ```

   This installs both the local `krun-helper` system service and the in-cluster traffic manager. Re-running the command upgrades both.

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

3. Check helper and runtime status (without starting them):

```sh
krun status
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

- `build [--skip-web, --force, --flush] <project|service>`  
  Build a project or specific service.
  Builds run inside a persistent `docker-build` pod in your cluster that caches layers and workspace files between builds. Use `--skip-web` to skip building the web service, `--force` to force a rebuild even if the source has not changed, and `--flush` to delete the existing build pod before building (clearing any cached layers or copied workspace files).

  ```sh
  krun build awesome-app
  krun build awesome-app-api
  krun build --skip-web awesome-app
  krun build --skip-web --force awesome-app
  krun build --flush awesome-app
  ```

- `deploy [--use-remote-registry, --no-restart] <project>`  
  Deploy a project.  
  Use `--use-remote-registry` to deploy using the remote Docker registry defined in `config.json`. If not specified, it will use the local Docker registry. Use `--no-restart` to skip the rollout restart after applying manifests.

  ```sh
  krun deploy awesome-app
  krun deploy --use-remote-registry awesome-app
  krun deploy --no-restart awesome-app
  ```

- `delete <project>`
  Delete a deployed project and its resources from the cluster.

  ```sh
  krun delete awesome-app
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

  > NOTE: Debug mode launches the `krun-helper` daemon which requires **elevated privileges** (Windows UAC / Linux sudo) to modify your hosts file and set up port-forwards.

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

- `install`
  Install or upgrade both the local `krun-helper` system service and the in-cluster debug runtime. Idempotent — re-run to upgrade.

  ```sh
  krun install
  ```

- `uninstall`
  Remove the local `krun-helper` system service and the in-cluster debug runtime.

  ```sh
  krun uninstall
  ```

- `status`
  Report health and version of the local `krun-helper` service and the in-cluster runtime. Does not start the helper.

  ```sh
  krun status
  ```

## config.json

The `config.json` file configures how `krun` interacts with your Kubernetes environment, Docker registries, and source code. This file must be placed in `$HOME/.krun/config.json` on Linux, or `%USERPROFILE%\.krun\config.json` on Windows. The install scripts create a default file if one does not already exist. Below is a description of each field:

### Example

```json
{
  "source": {
    "path": "c:/git/",
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

The `krun.json` file is used by `krun` to detect available services in your defined source folder. It should be placed in the root of your project repository and contains an array of service definitions.

The **project name** is derived from the directory that contains the `krun.json` file. For example, if the file is located at `c:/git/awesome-app/krun.json`, the project name will be `awesome-app`. This is the name you use in commands like `krun build awesome-app` and `krun deploy awesome-app`.

### Example

```json
[
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
```

### Field Reference

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

Debug mode requires **elevated privileges** because the `krun-helper` daemon modifies your hosts file and sets up port-forwards. On Windows you will see a UAC prompt, on Linux/macOS the helper is started with `sudo`.

Install the helper service and runtime components once per machine/cluster:

```sh
krun install
```

### Enabling Debug Mode

To enable debug mode for a service, use the following command:

```sh
krun debug enable <service>
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


## krun-helper API

`krun-helper` exposes the same local HTTP API that the `krun` CLI uses for workspace operations and debug session management. That makes it a good fit for editor integrations, shell scripts, and other developer-machine automation.

By default the helper listens on `http://127.0.0.1:47831` and only accepts loopback addresses. Install it with `krun install`, then point scripts or editor integrations at that local endpoint.

The full API spec, payload examples, and SSE details live in [docs/krun-helper-api.md](docs/krun-helper-api.md).

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Lightweight health check. Returns `{"success":true,"message":"ok"}` when the daemon is ready. |
| `GET` | `/v1/workspace/services` | Discover projects and services from the workspaces defined in `config.json`. |
| `GET` | `/v1/workspace/service/{serviceName}` | Fetch a single service definition by name. |
| `POST` | `/v1/workspace/build` | Build a project or a single service. Response is an SSE stream. |
| `POST` | `/v1/workspace/deploy` | Deploy a project. The `target` can be either the project name or any service in that project. Response is an SSE stream. |
| `POST` | `/v1/workspace/delete` | Delete a project. The `target` can be either the project name or any service in that project. Response is an SSE stream. |
| `GET` | `/v1/debug/sessions` | List the active debug sessions currently tracked by the local helper. |
| `POST` | `/v1/debug/enable` | Enable a local debug session: hosts file entries, dependency port-forwards, and traffic-manager session. |
| `POST` | `/v1/debug/disable` | Disable a local debug session and clean up local state. |
