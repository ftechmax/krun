# krun

A command-line tool for building, deploying, and debugging microservices in Kubernetes environments.

## Features

- Build Docker images for services
- Deploy services to Kubernetes
- Enable/disable debug mode for services using the in-cluster krun runtime

## Installation

1. Get the latest version of `krun` from the [releases page](https://github.com/ftechmax/krun/releases).

2. Unzip the downloaded file to a directory in your `PATH`, or add the directory to your `PATH`.

3. Place a `krun.json` file in the root of your project repository to define your services. See the [krun.json](#krunjson) section for details.

4. Install the runtime components in your cluster

   ```sh
   krun debug runtime install
   ```

   You can also apply the release manifest directly if you prefer:

   ```sh
   kubectl apply -f https://github.com/ftechmax/krun/releases/latest/download/krun-traffic-manager.yaml
   ```

5. Follow the [debugging instructions](#debugging-with-krun-runtime) to set up your project for debugging with the krun runtime.

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

- `build [--skip-web, --force, --flush] <project|service>`  
  Build a project or specific service.  
  Use `--skip-web` to skip building the web service, `--force` to force a rebuild even if the source has not changed, and `--flush` to delete the existing build pod before building (clearing any cached layers or copied workspace files).

  ```sh
  krun build awesome-app
  krun build --skip-web awesome-app
  krun build --skip-web --force awesome-app
  krun build --flush awesome-app
  ```

- `deploy [--use-remote-registry, --no-restart] <project>`  
  Deploy a project.  
  Use `--use-remote-registry` to deploy using the remote Docker registry defined in `krun-config.json`. If not specified, it will use the local Docker registry. Use `--no-restart` to skip the rollout restart after applying manifests.

  ```sh
  krun deploy awesome-app
  krun deploy --use-remote-registry awesome-app
  krun deploy --no-restart awesome-app
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
  Check whether the local elevated `krun-helper` daemon is currently running.

  ```sh
  krun debug helper status
  ```

- `debug runtime install`  
  Install or upgrade the in-cluster debug runtime resources.

  ```sh
  krun debug runtime install
  ```

- `debug runtime status`  
  Check if the in-cluster debug runtime is healthy and version-aligned.

  ```sh
  krun debug runtime status
  ```

- `debug runtime uninstall`  
  Remove the in-cluster debug runtime resources.

  ```sh
  krun debug runtime uninstall
  ```

## Examples

1. Build the entire project:

   ```sh
   krun build awesome-app
   ```

1. Build the project without the web service:

   ```sh
   krun build --skip-web awesome-app
   ```

1. Build a specific service:

   ```sh
   krun build awesome-app-api
   ```

1. Deploy a service:

   ```sh
   krun deploy awesome-app-api
   ```

1. Enable debug mode:

   ```sh
   krun debug enable awesome-app-api
   ```

1. Enable debug mode for specific container:

   ```sh
   krun debug enable awesome-app-api --container mysidecar
   ```

1. Disable debug mode:

   ```sh
   krun debug disable awesome-app-api
   ```

1. Check helper daemon status:

```sh
krun debug helper status
```

## krun-config.json

The [krun-config.json](https://github.com/ftechmax/krun/blob/main/krun-config.json) file configures how `krun` interacts with your Kubernetes environment, Docker registries, and source code. Below is a description of each field:

### Example

```json
{
  "source": {
    "path": "c:/git/",
    "search_depth": 1
  },
  "hostname": "kube.local",
  "local_registry": "registry:5000",
  "remote_registry": "docker.io/ftechmax"
}
```

### Field Reference

- **`source`**
  - `path`: Root directory where your project source code is located.
  - `search_depth`: How many directory levels to search for services in `path`.

- **`hostname`**: The hostname of the Kubernetes server.

- **`local_registry`**: Address of your local Docker registry (used for local builds).

- **`remote_registry`**: Address of the remote Docker registry (used for deploying non-local builds).

> Notes  
> Ensure all paths use forward slashes or are properly escaped for your operating system.  
> Make sure your kubeconfig is correctly configured.

## krun.json

The `krun.json` file is used by `krun` to detect available services in your project. It should be placed in the root of your project repository and contains an array of service definitions.

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
    "context": "src/"
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
  - Hosts are expanded with `service.svc` and `service` aliases to match common connection string formats.

Example host expansion:

```text
Input:  rabbitmq.default.svc
Adds:   rabbitmq.default.svc
        rabbitmq.svc
        rabbitmq
```

## Debugging with krun Runtime

To debug a service using the krun runtime, first install the runtime components in your cluster and then enable debug mode for the target service.

### Prerequisites

Ensure your project launches as a console application (which is the default profile).

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

To inject Kubernetes environment variables into your project when in debug mode, you have to consume the `appsettings-debug.env` file that the debug mode creates in your solution directory.

Add the following in your `program.cs` file.

#### For WebApplication builder

```csharp
var builder = WebApplication.CreateBuilder(args);

ConfigureConfiguration(builder);
```

```csharp
private static void ConfigureConfiguration(WebApplicationBuilder builder)
{
    if (!builder.Environment.IsDevelopment())
    {
        return;
    }

    // Read appsettings-debug.env and inject each line as environment variable
    var envFile = Path.Combine("../appsettings-debug.env");
    if (File.Exists(envFile))
    {
        foreach (var line in File.ReadAllLines(envFile))
        {
            var trimmed = line.Trim();
            if (string.IsNullOrWhiteSpace(trimmed) || trimmed.StartsWith("#")) continue;
            var separatorIndex = trimmed.IndexOf('=');
            if (separatorIndex > 0)
            {
                var key = trimmed.Substring(0, separatorIndex).Trim();
                var value = trimmed.Substring(separatorIndex + 1).Trim();
                if (!string.IsNullOrEmpty(key))
                {
                    Environment.SetEnvironmentVariable(key, value);
                }
            }
        }
    }

    // Read environment variables into the configuration
    builder.Configuration.AddEnvironmentVariables();
}
```

#### For Default Host builder

```csharp
var builder = Host.CreateDefaultBuilder(args);

builder.ConfigureAppConfiguration(ConFigureConfiguration);
```

```csharp
private static void ConFigureConfiguration(HostBuilderContext context, IConfigurationBuilder config)
{
    if (!context.HostingEnvironment.IsDevelopment())
    {
        return;
    }

    // Read appsettings-debug.env and inject each line as environment variable
    var envFile = Path.Combine("../appsettings-debug.env");
    if (File.Exists(envFile))
    {
        foreach (var line in File.ReadAllLines(envFile))
        {
            var trimmed = line.Trim();
            if (string.IsNullOrWhiteSpace(trimmed) || trimmed.StartsWith("#")) continue;
            var separatorIndex = trimmed.IndexOf('=');
            if (separatorIndex > 0)
            {
                var key = trimmed.Substring(0, separatorIndex).Trim();
                var value = trimmed.Substring(separatorIndex + 1).Trim();
                if (!string.IsNullOrEmpty(key))
                {
                    Environment.SetEnvironmentVariable(key, value);
                }
            }
        }
    }

    // Read environment variables into the configuration
    config.AddEnvironmentVariables();
}
```
