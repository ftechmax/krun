# Build and deploy

Build and deploy use the shared `internal/kube` client. Kustomize rendering runs
in-process, and Kubernetes operations use typed and dynamic clients without
shelling out to `kubectl`.

## Build pod

`krun build` reuses `default/docker-build` to cache image layers and workspace
files. The embedded manifest in `internal/krun/build/buildpod.go` defines an
unprivileged rootless BuildKit container and a `buildkitd.toml` ConfigMap that
marks `registry:5000` as plain HTTP. The workspace and BuildKit cache use
`emptyDir`, so deleting the pod clears them.

The build path applies the manifest when the pod is not Running and watches for
`PodReady=True`. A terminating pod still reports phase Running, so krun waits
until it is gone before recreating it. `--flush` deletes the build resources
before recreating them.

## Source sync

Source files are synced to `/var/workspace/<project>` through pod exec.

1. List remote files with `find`, collecting relative paths, modification times,
   and sizes. A missing project directory is treated as empty.
2. Walk the local project and apply the exclusions below.
3. Upload files that are missing remotely, have a different size, or have a newer
   local modification time (compared at whole-second precision).
4. Stream changed files as tar+gzip to `tar xzf - -C /var/workspace`.
5. Delete remote files absent from the local set. Deletion paths must remain
   under the remote project root and are passed as NUL-delimited arguments.

Equal-size files with equal or older local timestamps are skipped; sync does
not compare file contents. Output uses `+` for additions, `~` for updates, and
`-` for deletions. With no changes, image building is skipped unless `--force`
is set.

### Exclusions

A project-root `.dockerignore` defines the source exclusions using Docker's
pattern matcher, including `**`, character classes, and `!` negations.

Without a `.dockerignore`, krun excludes `.github`, `.vs`, `.git`, `.angular`,
`bin`, `obj`, `node_modules`, `k8s`, and `docs` at any depth. When a
`.dockerignore` exists, it replaces these defaults. Include those patterns
in the file if you still want them excluded.

Each selected service's Dockerfile is always synced because BuildKit
reads it from the workspace. Previously synced files that become excluded
are deleted on the next sync.

## Image build and push

`buildctl` runs inside the build pod with output streamed to the terminal. Each
selected service is built from its configured Dockerfile and context and pushed
to the configured registry with the `latest` tag in one step. If any service
fails, `krun build` exits with status 1.

## Deploy and remove

The project path comes from service discovery. Deploy and remove use
`<overlays_dir>/<name>`, selected with `--overlay`. The name defaults to `local`
and can be any single directory name, such as `staging` or `preview-123`.

Set `overlays_dir` in the project's `krun.json` to change the directory:

```json
{
  "overlays_dir": "deploy/overlays",
  "services": [
    { "name": "my-app-api", "path": "src/api", "dockerfile": ".", "context": "." }
  ]
}
```

The directory defaults to `k8s/overlays/`. Relative paths are resolved from the
directory containing `krun.json`; absolute paths are also accepted.

Common overlay names:

| Overlay | Target |
| --- | --- |
| `local` (default) | Local machine development |
| `development` | Dev/test server |
| `production` | Production server |

```sh
krun deploy my-app
krun deploy my-app --overlay development --use-remote-registry
krun deploy my-app --overlay production --use-remote-registry
krun remove my-app --overlay development
```

Overlay selection is independent of registry selection and kubeconfig.
Select the target cluster with `--kubeconfig`. `delete` remains an alias for
removal. The `--environment` flag has been replaced by `--overlay`.

krun renders the selected overlay, replaces local-registry references
with the selected registry, and applies or deletes the objects through the
dynamic client and REST mapper. Apply uses server-side apply with field manager
`krun` and forced field ownership; deletion uses background propagation and
ignores resources that are already absent.

`--create-namespace` creates missing target namespaces, excluding namespaces
supplied by the manifests. Without the flag, krun does not make these extra
Namespace API calls.

After apply, Deployment and StatefulSet targets receive a rollout-restart
annotation unless `--no-restart` is set. Targets absent at restart time are
skipped. `--use-remote-registry` selects the remote registry from
`krun-config.json`.
