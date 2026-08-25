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
| `timeout` | Per-backend HTTP timeout |
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
| `store` | `HAP_STORE` | `sqlite` |
| `sqlite.path` | `HAP_SQLITE_PATH` | `./data/affinity.db` |
| `postgres.url` | `HAP_DATABASE_URL` | empty |
| `token_ttl` | | `720h` |
| `device_ttl` | | `720h` |
| `anon_ttl` | | `15m` |

Switching SQLite ↔ Postgres does not migrate bindings.

Gray-list classification is MediaBrowser `Client=`, configured path prefixes, and the stored token/DeviceId client — not `userId`. Hop tweaks (no upstream keep-alive, immediate flush) apply automatically when a request is gray-listed.

## Health

See [health.md](health.md). Interval default `10s`. Optional `auth_probe` is off by default.
