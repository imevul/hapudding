# Configuration

YAML file (`--config`) plus environment overrides (env wins).

## Top level

| Key | Env | Default | Notes |
| --- | --- | --- | --- |
| `listen` | `HAP_LISTEN` | `:8096` | Public Jellyfin-facing bind |
| `status.listen` | `HAP_STATUS_LISTEN` | `127.0.0.1:9100` | Ops JSON + Prometheus, not an HTML UI |

## Backends

One or more. Names are operator-chosen.

| Field | Purpose |
| --- | --- |
| `name` | Stable id used in affinity and logs |
| `url` | Upstream base URL (required) |
| `headers` | Extra hop headers. **Rejected** if they collide with `Authorization`, `X-Emby-Authorization`, `X-Emby-Token`, `X-MediaBrowser-Token` |
| `host` | Override upstream `Host` (and SNI unless `tls.server_name`) |
| `tls.ca_file` / `insecure_skip_verify` / `client_cert` + `client_key` / `server_name` | TLS hop options |
| `timeout` | Per-backend HTTP timeout (health client and proxied `ResponseHeaderTimeout`; default `60s`). Login uses `performance.auth_timeout` instead. |
| `disabled` | Park this backend at startup (`false`). Runtime overlay via `POST /hap/backends/{name}/disable` can also park it. See [health.md](health.md). |
| `health_url` | Optional probe base if different from `url` |

Hop header secrets via env: `HAP_BACKEND_<NAME>_HEADER_<HEADER>` (name uppercased, `-` → `_`).

Do not set hop-level `Authorization`. Clients already send Jellyfin's MediaBrowser token on that field.

## Affinity

| Key | Env | Default |
| --- | --- | --- |
| `policy` | `HAP_AFFINITY_POLICY` | `force_reauth` |
| `graylist.policy` | `HAP_GRAYLIST_POLICY` | `fail_closed` |
| `graylist.clients` | | omit = `[Infuse]`; `[]` disables built-in names |
| `graylist.path_prefixes` | | extras; `/InfuseSync` is implied while Infuse is in `clients` |
| `new_clients_require` | `HAP_NEW_CLIENTS_REQUIRE` | `healthy` |
| `store` | `HAP_STORE` | `postgres` |
| `sqlite.path` | `HAP_SQLITE_PATH` | `./data/affinity.db` |
| `postgres.url` | `HAP_DATABASE_URL` | `postgres://hap:hap@localhost:5432/hap?sslmode=disable` |
| `token_ttl` | | `720h` |
| `device_ttl` | | `720h` |
| `anon_ttl` | | `15m` |

Postgres is the default affinity store. Compose sets `HAP_DATABASE_URL` to the `postgres` service (`postgres://hap:hap@postgres:5432/hap?sslmode=disable`). SQLite remains available (`affinity.store: sqlite` / `HAP_STORE=sqlite`) and opens with WAL, `busy_timeout=5000`, and a single writer connection. Switching drivers does not migrate bindings.

Gray-list classification is MediaBrowser `Client=`, configured path prefixes, and the stored token/DeviceId client — not `userId`. Hop tweaks (no upstream keep-alive, immediate flush) apply automatically when a request is gray-listed.

## Performance

All optional proxy mitigations live under `performance`. Toggles that default on use explicit `enabled: false` to turn off. A hand-built `Config` that never calls `Load()` leaves them off.

| Key | Env | Default |
| --- | --- | --- |
| `performance.auth_timeout` | `HAP_AUTH_TIMEOUT` | `60s` (login / Quick Connect hops only) |
| `performance.cache.enabled` | `HAP_CACHE_ENABLED` | **true** |
| `performance.cache.max_bytes` | | `268435456` (256MiB) |
| `performance.cache.max_object` | | `2097152` (2MiB) |
| `performance.cache.default_ttl` | | `15m` (untagged `Cache-Control: public`) |
| `performance.cache.max_ttl` | | `24h` |
| `performance.library.enabled` | `HAP_LIBRARY_CACHE_ENABLED` | **true** |
| `performance.library.ttl` | | `30s` |
| `performance.library.max_bytes` | | `67108864` (64MiB) |
| `performance.library.max_object` | | `4194304` (4MiB) |
| `performance.coalesce.enabled` | `HAP_COALESCE_ENABLED` | **true** |
| `performance.warm_login.enabled` | `HAP_WARM_LOGIN_ENABLED` | `false` |
| `performance.library_concurrency.enabled` | `HAP_LIBRARY_CONCURRENCY_ENABLED` | `false` |
| `performance.library_concurrency.max` | | `3` (total per backend, not per user; queue at cap) |

**Image cache** is an in-memory LRU for `/Items/{id}/Images/…` only. Key is backend + path + query + `Accept`. User avatars, video/audio, and JSON listings are not stored there.

**Library cache** is token-keyed (`backend + sha256(token) + method + path + query`). Allowlist: `Views`, `Resume`, `NextUp`, `Latest`. Playback / UserData writes drop that token’s entries. Never shared across users or backends.

**Coalesce** shares one in-flight image or library GET among waiters. **Warm login** (off) prefetches Views/Resume/NextUp after a successful login into the library cache. **Library concurrency** (off) queues extra library hops per backend.

`GET /hap/cache` on the status bind is image stats. `GET /hap/performance` includes images, library, coalesce, concurrency, and `auth_timeout`.

Top-level `cache:` is ignored (moved under `performance`).

## Health

See [health.md](health.md). Interval default `10s`. Optional `auth_probe` is off by default.
