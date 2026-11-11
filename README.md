# krun

Build, deploy, and debug Kubernetes services from one CLI. Build images inside
the cluster without local Docker, deploy Kustomize overlays, and route cluster
traffic to a local app for debugging.

## Installation

1. Download the archive for your platform from [Releases](https://github.com/ftechmax/krun/releases).
2. Extract `krun` and `krun-helper` together into a directory on your `PATH`.
3. Create the two configuration files below.

You need a Kubernetes cluster and a container registry reachable from it.
krun uses `~/.kube/config`; pass `--kubeconfig <path>` to select another file.

## Configuration

### krun-config.json

Place this file next to the `krun` binary:

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

`source.path` is the directory to search for projects; `search_depth` sets how
many directory levels to search. Builds push to `local_registry`. Deploy uses
that registry unless you pass `--use-remote-registry`.

### krun.json

Place this file in your project root. The directory name becomes the project
name: `~/git/my-app/krun.json` defines the project `my-app`.

```json
{
  "overlays_dir": "k8s/overlays/",
  "services": [
    {
      "name": "my-app-api",
      "namespace": "default",
      "path": "src/api",
      "dockerfile": ".",
      "context": ".",
      "container_port": 8080,
      "intercept_port": 5000,
      "service_dependencies": [
        { "host": "rabbitmq.default.svc", "port": 5672 }
      ]
    }
  ]
}
```

`overlays_dir` defaults to `k8s/overlays/`. Relative paths start at the project
root; absolute paths are also accepted. `--overlay staging` selects its
`staging` subdirectory; the default overlay is `local`.

Each entry in `services` configures one service:

| Field | Meaning |
| --- | --- |
| `name` | Service name used in commands; match the deployed workload's name. |
| `namespace` | Kubernetes namespace. Default: `default`. |
| `path` | Service directory, relative to the project root. |
| `dockerfile` | Directory containing `Dockerfile`, relative to `path`. |
| `context` | Build context, relative to the project root. |
| `container_port` | Port intercepted in the pod. Default: `8080`. |
| `intercept_port` | Local app port. Default: `5000`; use a different port for each service you debug together. |
| `service_dependencies` | Cluster services to forward locally while debugging. |

Each dependency needs a `host` and `port`. Set `service` and `namespace` if they
cannot be inferred from the host. Optional `aliases` lists additional hostnames
that should use the same forward.

## Build and deploy

```sh
krun list
krun build my-app
krun deploy my-app --overlay local
krun remove my-app --overlay local
```

`build` also accepts a service name. Use `--force` to rebuild unchanged source
or `--flush` to clear the cluster build cache. A project-root `.dockerignore`
controls which source files are synced.

Deploy applies the selected overlay and restarts its Deployments and
StatefulSets. Options:

- `--use-remote-registry`: replace local registry references with `remote_registry`.
- `--no-restart`: skip the rollout restart.
- `--create-namespace`: create missing target namespaces.

`delete` is another name for `remove`.

## Debug

Set `intercept_port` to the port your local app listens on. Install the debug
runtime once per cluster, then enable a service:

```sh
krun debug runtime install
krun debug enable my-app-api
```

Enable writes the pod's app environment to `.env` in the service directory.
Configure your app to load that file, then run it locally in your IDE. Cluster
traffic now reaches your local app; `service_dependencies` are forwarded from
the cluster and their hostnames resolve locally.

Use `--container <name>` with `debug enable` to select a container in a
multi-container pod. The first enable prompts for elevation (Windows UAC or
Linux sudo) so the hosts writer can update your hosts file.

```sh
krun debug list
krun debug disable my-app-api
```

Disable restores cluster traffic and removes the generated `.env` file.

| Command | Purpose |
| --- | --- |
| `krun debug helper status` | Check the local helper. |
| `krun debug helper stop` | Stop the helper and hosts writer; remove krun's hosts entries. |
| `krun debug runtime status` | Check the cluster runtime. |
| `krun debug runtime uninstall` | Remove the cluster runtime. |

Use `krun <command> --help` for command options. See [build and deploy](docs/build-deploy.md),
[debugging architecture](docs/debug.md), or [development](docs/development.md)
for more detail.
