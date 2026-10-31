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

The built-in excluded directories are `.github`, `.vs`, `.git`, `.angular`,
`bin`, `obj`, `node_modules`, `k8s`, and `docs`. `--skip-web` also excludes `web`
and skips services whose path ends in `web`.

A project-root `.dockerignore` adds exclusions:

- Patterns are relative to the project root: `*.log` matches top-level logs;
  `**/*.log` matches at any depth.
- `*` and `?` do not cross `/`; `**` spans any number of path segments.
- A directory match excludes its descendants.
- Lines starting with `#` are comments. A leading `!` re-includes a path;
  the last matching pattern wins.

Character classes and backslash escapes are unsupported. Negations cannot
override built-in exclusions. Previously synced files that become ignored are
deleted on the next sync.

Each selected service's Dockerfile is always synced, even when an exclusion
matches it, because BuildKit reads it from the workspace.

## Image build and push

`buildctl` runs inside the build pod with output streamed to the terminal. Each
selected service is built from its configured Dockerfile and context and pushed
to the configured registry with the `latest` tag in one step. If any service
fails, `krun build` exits with status 1.

## Deploy and delete

The project path comes from service discovery. Overlay selection checks:

1. `k8s/cloud`: if present, both `k8s/cloud/overlays/local` and
   `k8s/edge/overlays/local` must exist.
2. `k8s/overlays/local`.
3. A Kustomization in `k8s` itself.

For each overlay, krun renders manifests, replaces local-registry references
with the selected registry, and applies or deletes the objects through the
dynamic client and REST mapper. Apply uses server-side apply with field manager
`krun` and forced field ownership; deletion uses background propagation and
ignores resources that are already absent.

`--create-namespace` checks and creates missing target namespaces in sorted
order, excluding namespaces supplied by the manifests. Without the flag, krun
does not make these extra Namespace API calls.

After apply, Deployment and StatefulSet targets receive a rollout-restart
annotation unless `--no-restart` is set. Targets absent at restart time are
skipped. `--use-remote-registry` selects the remote registry from
`krun-config.json`.
