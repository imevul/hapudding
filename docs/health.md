# Health

Layers, checked on `health.interval` (default 10s). Probes use the same hop TLS/headers as proxied traffic.

1. **Reachability** — HTTP connect to the instance
2. **Public liveness** — `GET /System/Info/Public` (caches reported Id/name/version; never rewritten on the client path)
3. **Jellyfin `/health`** — HTTP + database connectivity
4. **Auth-plane** — passive: N recent 5xx on login/user endpoints in a window → **degraded**. Optional active `AuthenticateByName` probe (off by default)

States: `healthy`, `degraded`, `unhealthy`, `unknown`.

Public info 200 is not enough. A backend can answer `/System/Info/Public` while login returns 500.

Backend 4xx/5xx are **proxied as-is**. HAP synthesizes 503/401 only when it cannot reach a backend or policy refuses to rebind. Logs include the backend name. Never collapse a Jellyfin 500 into a generic 502.

## Operator disable

`backends[].disabled: true` parks a backend at startup. `POST /hap/backends/{name}/disable` on the **status bind** (default `127.0.0.1:9100`) writes a store overlay that survives restart. `POST …/enable` clears the overlay only — YAML `disabled: true` stays parked (`409`).

Disabled is stronger than unhealthy: HAP never hops there (including `pin_unhealthy` and login failover). Bound clients get `503 backend_disabled`. Health probes still run. `GET /hap/backends` includes `disabled`, `config_disabled`, `runtime_disabled`, and `ineligible_reason: "disabled"`.

Host helpers: `make backends`, `make backend-disable NAME=…`, `make backend-enable NAME=…` (`STATUS` defaults to `http://127.0.0.1:9100`). Compose publishes status as `127.0.0.1:9100:9100` while the process listens on `0.0.0.0:9100` inside the container.
