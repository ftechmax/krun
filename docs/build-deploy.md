# Build and Deploy (Current Implementation)

This document describes the current `krun build` and `krun deploy` behavior in this repo.

## Current State

- Build pod lifecycle is migrated to `client-go` (`internal/kube` shared client).
- Source sync is migrated from SFTP/port-forwarding to pod exec + tar streaming.
- Build command execution is migrated to pod exec via `client-go` `remotecommand`.
- Deploy should use the same `client-go` + dynamic client approach (no `kubectl` dependency).

The remaining deploy work is to make this flow fully client-based end-to-end.

---

## Shared Kubernetes Client

`internal/kube/client.go` exposes:

- `RestConfig`
- `Clientset`
- `DynamicClient`
- `Mapper` (deferred, memory-cached REST mapper)

This client is used by the refactored build pod lifecycle and source sync exec operations.

---

## Build Flow

## 1. Build Pod Existence Check

`buildPodExists` uses typed client `Get` for `default/docker-build`.

- `NotFound` -> pod does not exist.
- Exists and `Status.Phase == Running` -> ready for use.

## 2. Build Pod Apply

`createBuildPod` decodes the embedded multi-doc YAML manifest and applies each object with:

- dynamic client
- REST mapper resolution by GVK
- Server-Side Apply (`ApplyPatchType`, field manager `krun`, force=true)

Current manifest includes:

- `ConfigMap` (`docker-build-registries`)
- `Pod` (`docker-build`) with:
  - `docker-build` container (buildah only)

## 3. Wait for Pod Ready

`waitForBuildPodReady` uses `watchtools.UntilWithSync` and waits for `PodReady=True` on `docker-build`.

## 4. Build Pod Delete

`deleteBuildPod` decodes the same manifest and deletes each object with dynamic client:

- propagation policy: `Background`
- `NotFound` ignored

## 5. Wait for Deletion

`waitForBuildPodDeletion` (in `build.go`) polls `buildPodExists` until timeout.

## 6. Source Sync (Stat-Based Remote-First)

`copySource` now uses exec on the build pod, not SFTP.

### 6.1 Remote listing

Exec command:

```sh
find /var/workspace/<project> -type f -printf '%P\t%T@\t%s\n'
```

Parsed into:

- relative path
- mtime (ns)
- size (bytes)

If the project directory does not exist remotely, sync treats remote as empty (first run / after flush).

### 6.2 Local listing

Walk local project root and collect file metadata.

Excluded directories:

- `.github`
- `.vs`
- `.git`
- `.angular`
- `bin`
- `obj`
- `node_modules`
- `k8s`
- `docs`

`web` is excluded when `--skip-web` is used.

### 6.3 Delta rule (matches old SFTP semantics)

A file is uploaded when:

- it does not exist remotely, or
- size differs, or
- local mtime (seconds precision) is newer than remote mtime

If local is equal/older and size matches, upload is skipped.

### 6.4 Upload

Changed files are tar+gzip streamed over exec:

- writer: local goroutine (`archive/tar` + `compress/gzip`)
- extractor in pod: `tar xzf - -C /var/workspace`
- tar paths are rooted under `<project>/...`

### 6.5 Delete

Remote files not present locally are deleted via exec:

- command: `xargs -0 -r rm -rf --`
- paths are sanitized and must remain under `/var/workspace/<project>/`

### 6.6 Sync output semantics

- `+` added (remote missing)
- `~` updated (remote existed and upload triggered)
- `-` deleted (stale remote file removed)

No deltas -> `No changes - project up to date`.

## 7. Build and Push

`buildAndPushImagesBuildah` runs buildah inside the build pod using `remotecommand`
with stdout/stderr streamed to the local terminal:

- `/bin/sh -c "buildah bud ... && buildah push ..."`

---

## Deploy Flow (Current)

Deploy should follow the same Go Kubernetes client approach used by build:

- render: kustomize libraries in-process (no `kubectl kustomize` shell-out)
- registry replacement: mutate rendered objects in memory before apply
- apply/delete: decode manifests and use dynamic client + REST mapper for server-side apply and delete
- restart: only for Deployment/StatefulSet targets that already exist in the cluster (patch pod template annotation)

Result: deploy no longer depends on the `kubectl` binary and uses the shared `internal/kube` client stack end-to-end.

---

## Validation Performed

The new sync behavior was validated using:

```sh
go run ./cmd/krun build awesome-app3-api --force --skip-web
```

Observed results:

- Add file (`src/api/AwesomeApp3.Api/Controllers/TestController.cs`) -> `Added: 1 Updated: 0 Deleted: 0`
- Modify file (`src/api/AwesomeApp3.Api/Controllers/ExampleController.cs`) -> `Added: 0 Updated: 1 Deleted: 0`
- Delete file (`src/api/AwesomeApp3.Api/Controllers/TestController.cs`) -> `Added: 0 Updated: 0 Deleted: 1`
- No changes -> `No changes - project up to date`

---

## Dependencies

Build refactor relies on:

- `k8s.io/client-go`
- `k8s.io/apimachinery`
- `k8s.io/api`

Notably, SFTP/SSH are no longer used by source sync runtime logic.
