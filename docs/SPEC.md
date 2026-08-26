# HAP specification

HAP (Highly Available Pudding) is a Jellyfin-aware reverse proxy for **one or more** independent Jellyfin instances.

## Constraints

- Server IDs, user IDs, item IDs, and tokens are backend-local. HAP does not rewrite them unless `translate.server_id` is enabled, and then only the top-level `Id` on `GET /System/Info` and `GET /System/Info/Public` (optional `name` may also replace `ServerName`).
- Never request-level round-robin an authenticated client.
- Token affinity wins; DeviceId is fallback; header-less media may follow a live session (cookie if that backend has a token/DeviceId, else IP-only glue); short-lived IP+User-Agent glue last. Unauthenticated login may retry other eligible backends on hop timeout or 401. Config `user_affinity` may prefer a backend on first password login; it does not override token/device pins or eligibility.
- Cookie `hap_backend` is a hint only, never the sole affinity source. HAP sets it on proxied responses. Image/stream requests may use it only when that backend already has a live token or DeviceId binding.
- Backend HTTP errors are proxied as-is. HAP only synthesizes its own 503/401 when it cannot reach a backend or policy refuses to rebind.
- `/System/Info/Public == 200` is not sufficient health. Auth-plane failures can mark a backend **degraded**.
- Hop-level `Authorization` / Jellyfin token headers are rejected at config load.
- Gray-list (default Infuse) may use a different affinity policy (`fail_closed` by default). Classification is `Client=` / path / stored client, not `userId`.

## Status bind

Separate listener (default `127.0.0.1:9100`). `GET /` is a small HTML ops page (loopback by default; no extra auth). JSON paths are unchanged. Binding `status.listen` off loopback exposes the page and the POSTs below.

| Path | Purpose |
| --- | --- |
| `GET /` | HTML ops dashboard (status bind only) |
| `GET /ui/*` | Embedded dashboard assets |
| `GET /hap/health` | Process liveness |
| `GET /hap/ready` | Store usable and at least one backend eligible for new clients. A 1-backend `fail_closed`/`pin_unhealthy` pool is also ready when that instance is healthy, degraded, or still unknown (bound traffic may work). |
| `GET /hap/backends` | Per-backend health layers |
| `GET /hap/affinity` | Binding counts |
| `GET /hap/users` | Debug user/session list |
| `GET /hap/users/{userId}` | Debug dump; `?backend=` filters |
| `GET /hap/cache` | Image cache stats (memory + disk path/bytes/hits); not affinity |
| `GET /hap/performance` | Image + library cache, coalesce, concurrency, `auth_timeout` |
| `POST /hap/backends/{name}/disable` | Runtime-park a backend (store overlay; status bind only) |
| `POST /hap/backends/{name}/enable` | Clear runtime overlay (`409` if YAML `disabled: true`) |
| `GET /hap/user-affinity` | Configured `- username: backend` login hints |
| `POST /hap/users/by-name/{username}/unpin` | Delete stored token pins for a username (and DeviceId pins on those backends) |
| `GET /metrics` | Prometheus |

`/hap/*` and `/metrics` 404 on the public Jellyfin listener. Public `/` is still proxied to Jellyfin.

## Non-goals (v1)

- User/item ID translation or forcing matching Jellyfin database IDs (optional Server ID presentation on `/System/Info*` is listed above; it is not a merged library)
- Multi-HAP process cluster (expected later; Postgres is the default store)
- Public-listener admin UI, or rewriting `hap.yaml` from the status API (runtime disable is a store overlay)
- Replacing the operator's ingress
- Sharing one image or library cache across backends, or caching library JSON without a token in the key
