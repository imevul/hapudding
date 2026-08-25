# HAP specification

HAP (Highly Available Pudding) is a Jellyfin-aware reverse proxy for **one or more** independent Jellyfin instances.

## Constraints

- Server IDs, user IDs, item IDs, and tokens are backend-local. HAP does not rewrite them.
- Never request-level round-robin an authenticated client.
- Token affinity wins; DeviceId is fallback; short-lived IP+User-Agent glue last.
- Cookie `hap_backend` is a hint only, never the sole affinity source.
- Backend HTTP errors are proxied as-is. HAP only synthesizes its own 503/401 when it cannot reach a backend or policy refuses to rebind.
- `/System/Info/Public == 200` is not sufficient health. Auth-plane failures can mark a backend **degraded**.
- Hop-level `Authorization` / Jellyfin token headers are rejected at config load.
- Gray-list (default Infuse) may use a different affinity policy (`fail_closed` by default). Classification is `Client=` / path / stored client, not `userId`.

## Status bind

Separate listener (default `127.0.0.1:9100`). Not an HTML page.

| Path | Purpose |
| --- | --- |
| `GET /hap/health` | Process liveness |
| `GET /hap/ready` | Store usable and at least one backend eligible for new clients. A 1-backend `fail_closed`/`pin_unhealthy` pool is also ready when that instance is healthy, degraded, or still unknown (bound traffic may work). |
| `GET /hap/backends` | Per-backend health layers |
| `GET /hap/affinity` | Binding counts |
| `GET /hap/users` | Debug user/session list |
| `GET /hap/users/{userId}` | Debug dump; `?backend=` filters |
| `GET /metrics` | Prometheus |

These paths 404 on the public Jellyfin listener.

## Non-goals (v1)

- ID translation or forcing matching Jellyfin database IDs
- Multi-HAP process cluster (expected later; Postgres is already a store option)
- HTML admin UI
- Replacing the operator's ingress
