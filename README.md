# krun

A command-line tool for building, deploying, and debugging microservices in Kubernetes environments.

## Features

- Build Docker images for services
- Deploy services to Kubernetes
- Enable/disable debug mode for services using Telepresence

## Installation

1. Ensure you have the following dependencies installed:

   - [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)
   - [telepresence](https://telepresence.io/docs/quick-start/)

2. Get the latest version of `krun` from the [releases page](./releases).

3. Unzip the downloaded file to a directory in your `PATH`, or add the directory to your `PATH`.

4. Place a `krun.json` file in the root of your project repository to define your services. See the [krun.json](#krunjson) section for details.

5. Follow the [debugging instructions](#debugging-with-telepresence) to set up your project for debugging with Telepresence.

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

- `build [--skip-web, --force] <project|service>`  
  Build a project or specific service.  
  Use `--skip-web` to skip building the web service, and `--force` to force a rebuild even if the source has not changed.

  ```sh
  krun build awesome-app
  krun build --skip-web awesome-app
  krun build --skip-web --force awesome-app
  ```

- `deploy [--use-remote-registry] <project>`  
  Deploy a project.  
  Use `--use-remote-registry` to deploy using the remote Docker registry defined in `krun-config.json`. If not specified, it will use the local Docker registry.

  ```sh
  krun deploy awesome-app
  krun deploy --use-remote-registry awesome-app
  ```

- `debug list`  
  List all services with their debug status.

  ```sh
  krun debug list
  ```

- `debug enable <service> [--intercept, --container <container>]`  
  Enable debug mode for a service using Telepresence.  
  Use `--intercept` to intercept the service in the Kubernetes cluster, allowing you to run it both locally and in the cluster.  
  If `--intercept` is not specified, it will replace the service in the cluster and redirect traffic to your local debug session.

  ```sh
  krun debug enable awesome-app-api
  krun debug enable awesome-app-api --intercept
  ```

  Both replace and intercept mode need a port configured that matches the port your application is listening on. For example, a C# web api application usually listens on port `5000` which is defined in the `launchSettings.json` file. To make this available for krun, you need to add the `intercept_port` property in your `krun.json` file for the service you want to debug.

Use the `--container` option to specify which container to debug if your pod has multiple containers.

```sh
krun debug enable awesome-app-api --container awesome-app-api
```

- `debug disable <service>`  
  Disable debug mode for a service.

  ```sh
  krun debug disable awesome-app-api
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

1. Enable debug mode for specific conttainer:

   ```sh
   krun debug enable awesome-app-api --container mysidecar
   ```

1. Disable debug mode:
   ```sh
   krun debug disable awesome-app-api
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
    "container_port": 8080
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

- **`path`**: The relative path to the service directory from the root of your project. This is where the Dockerfile and source code for the service are located.

- **`dockerfile`**: The path where the Dockerfile is located relative to the `path` directory. If the Dockerfile is in the service directory, you can use `"."`.

- **`context`**: The build context for the Docker image. This is the directory that will be sent to the Docker daemon during the build process. If you have a shared project between multiple services, you can set this to the parent directory of the service directories as shown in the example.

- **`container_port`** (optional): The port of the container running inside the service pod. This is used for debugging and should match the port exposed by your service container.
  > NOTE: The default is `8080` if not specified.

## Debugging with Telepresence

To debug a service using Telepresence, you can enable debug mode for that service. This will allow you to run your project locally while still connected to the Kubernetes cluster.

### Prerequisites

Ensure your project launches as a console application (which is the default profile).

### Enabling Debug Mode

To enable debug mode for a service, use the following command:

```powershell
krun debug enable <service>
```

### Disabling Debug Mode

To disable debug mode for a service, use the following command:

```powershell
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
