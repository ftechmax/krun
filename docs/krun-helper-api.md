# krun-helper API

`krun-helper` exposes the same local HTTP API that the `krun` CLI uses for workspace operations and debug session management. This page contains the full automation reference.

By default the helper listens on `http://127.0.0.1:47831` and only accepts loopback addresses. The easiest way to use it is to install it once:

```sh
krun install
```

You can also run the helper directly when elevated. Pass the user config directory explicitly because elevated processes resolve their home directory differently:

```sh
krun-helper --config-path "$HOME/.krun"
```

The shared `config.json` and `token.bin` live in `$HOME/.krun/` on Linux, or `%USERPROFILE%\.krun\` on Windows. `--config-path` is required when starting `krun-helper`.

If you override the listen address with `--listen`, it still must be a loopback address such as `127.0.0.1:47831` or `localhost:47831`.

## API Conventions

- Send JSON request bodies with `Content-Type: application/json`.
- Request payloads are decoded strictly. Unknown JSON fields return `400 Bad Request`.
- Common status codes are `400` for invalid payloads or unknown targets, `405` for wrong HTTP methods, `500` for operation failures, and `503` when the helper cannot load workspace config or discover services.
- Most endpoints return JSON. Errors use the standard helper shape:

```json
{
  "success": false,
  "message": "..."
}
```

- `POST /v1/build`, `POST /v1/deploy`, and `POST /v1/delete` stream their output as Server-Sent Events (SSE). Use `curl -N` or an SSE-capable client.
- Streaming endpoints emit one `log` event per output chunk and finish with a `done` event:

```text
event: log
data: {"stream":"stdout","text":"building image...\n"}

event: done
data: {"ok":true}
```

If a streamed operation fails, the final event is:

```text
event: done
data: {"ok":false,"error":"..."}
```

## Endpoint Reference

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Lightweight health check. Returns `{"success":true,"message":"ok"}` when the daemon is ready. |
| `GET` | `/v1/workspace/list` | Discover projects and services from the current `config.json` workspace. |
| `GET` | `/v1/workspace/service/{serviceName}` | Fetch a single service definition by name. |
| `POST` | `/v1/workspace/build` | Build a project or a single service. Response is an SSE stream. |
| `POST` | `/v1/workspace/deploy` | Deploy a project. The `target` can be either the project name or any service in that project. Response is an SSE stream. |
| `POST` | `/v1/workspace/delete` | Delete a project. The `target` can be either the project name or any service in that project. Response is an SSE stream. |
| `GET` | `/v1/debug/sessions` | List the active debug sessions currently tracked by the local helper. |
| `POST` | `/v1/debug/enable` | Enable a local debug session: hosts file entries, dependency port-forwards, and traffic-manager session. |
| `POST` | `/v1/debug/disable` | Disable a local debug session and clean up local state. |

## Request and Response Shapes

### `GET /v1/workspace/list`

Returns discovered services and unique project names:

```json
{
  "services": [
    {
      "name": "awesome-app-api",
      "project": "awesome-app",
      "namespace": "default",
      "path": "src/api",
      "dockerfile": "AwesomeApp.Api",
      "context": "src/",
      "container_port": 8080,
      "intercept_port": 5000,
      "service_dependencies": []
    }
  ],
  "projects": ["awesome-app"]
}
```

### `GET /v1/workspace/service/{serviceName}`

Returns the definition of a single discovered service. The `serviceName` path segment must match the `name` field from a `krun.json` entry.

```json
{
  "name": "awesome-app-api",
  "project": "awesome-app",
  "namespace": "default",
  "path": "src/api",
  "dockerfile": "AwesomeApp.Api",
  "context": "src/",
  "container_port": 8080,
  "intercept_port": 5000,
  "service_dependencies": []
}
```

Returns `404 Not Found` with the standard error shape if no service matches the given name.

### `POST /v1/workspace/build`

Request body:

```json
{
  "target": "awesome-app",
  "skip_web": true,
  "force": true,
  "flush": false
}
```

`target` may be either a project name or a service name.

### `POST /v1/workspace/deploy`

Request body:

```json
{
  "target": "awesome-app",
  "use_remote_registry": true,
  "no_restart": false
}
```

`target` may be either a project name or a service name, but deploy always applies to the whole project.

### `POST /v1/workspace/delete`

Request body:

```json
{
  "target": "awesome-app"
}
```

`target` may be either a project name or a service name, but delete always applies to the whole project.

### `POST /v1/debug/enable`

Request body:

```json
{
  "session_key": "awesome-app/awesome-app-api",
  "context": {
    "project": "awesome-app",
    "service_name": "awesome-app-api",
    "namespace": "default",
    "container_port": 8080,
    "intercept_port": 5000,
    "service_dependencies": [
      {
        "host": "rabbitmq.default.svc",
        "port": 5672,
        "aliases": ["rabbitmq"]
      }
    ]
  }
}
```

`session_key` is optional. If you omit it, the helper derives one from the request context, usually `<project>/<service_name>`. For automation, sending an explicit `session_key` is the safest option.

Success response:

```json
{
  "success": true,
  "message": "debug enable applied"
}
```

### `GET /v1/debug/sessions`

Returns the sessions currently tracked by the helper:

```json
{
  "sessions": [
    {
      "session_key": "awesome-app/awesome-app-api",
      "context": {
        "project": "awesome-app",
        "service_name": "awesome-app-api",
        "namespace": "default",
        "container_port": 8080,
        "intercept_port": 5000,
        "service_dependencies": [
          {
            "host": "rabbitmq.default.svc",
            "port": 5672
          }
        ]
      }
    }
  ]
}
```

### `POST /v1/debug/disable`

You can disable a session by sending the same `session_key` that was used when the session was created:

```json
{
  "session_key": "awesome-app/awesome-app-api"
}
```

You can also omit `session_key` and provide enough context for the helper to resolve it:

```json
{
  "context": {
    "project": "awesome-app",
    "service_name": "awesome-app-api"
  }
}
```

If no matching local session exists, the helper returns success with `"message":"no active session"`.

## Examples

Health check:

```sh
curl -s http://127.0.0.1:47831/healthz
```

List discovered services:

```sh
curl -s http://127.0.0.1:47831/v1/services
```

Stream a build:

```sh
curl -N \
  -H 'Content-Type: application/json' \
  -H 'Accept: text/event-stream' \
  -d '{"target":"awesome-app","skip_web":true}' \
  http://127.0.0.1:47831/v1/build
```

Enable debug mode for a service:

```sh
curl -s \
  -H 'Content-Type: application/json' \
  -d '{
    "session_key": "awesome-app/awesome-app-api",
    "context": {
      "project": "awesome-app",
      "service_name": "awesome-app-api",
      "namespace": "default",
      "container_port": 8080,
      "intercept_port": 5000,
      "service_dependencies": [
        { "host": "rabbitmq.default.svc", "port": 5672 }
      ]
    }
  }' \
  http://127.0.0.1:47831/v1/debug/enable
```
