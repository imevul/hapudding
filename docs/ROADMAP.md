# HAP Roadmap

> Durable "what shipped and what remains." `[X]` landed. `[ ]` open.
> Update this file in the same change that ships, splits, or reschedules a line.

## Phase legend

- **P0 Foundation**: repo, Make, docs, CI, release-please, binary + alias, Compose
- **P1 Affinity**: auth parse, SQLite or Postgres store, lookup, three policies, client gray-list
- **P2 Proxy**: streaming HTTP/WS, login peek, honest errors
- **P3 Health**: layered probes, status JSON + Prometheus
- **Future**: multi-HAP, ID translation, HTML admin UI

---

## P0 - Foundation

| ID | Task | Status |
| --- | --- | --- |
| P0-1 | Git repo, MIT, module `github.com/imevul/hapudding` | [X] |
| P0-2 | Makefile, `hapudding` + `hap` alias, Docker/Compose, `.env.example` | [X] |
| P0-3 | Docs skeleton (SPEC, ROADMAP, config, affinity, health, clients), AGENTS | [X] |
| P0-4 | GitHub Actions + release-please | [X] |

## P1 - Affinity

| ID | Task | Status |
| --- | --- | --- |
| P1-1 | MediaBrowser / legacy / query parser | [X] |
| P1-2 | SQLite and Postgres store (token/device/anon, user debug fields) | [X] |
| P1-3 | Lookup order and policies `force_reauth`, `fail_closed`, `pin_unhealthy` | [X] |
| P1-4 | Client gray-list (Infuse default, `fail_closed`) + Infuse hop tweaks | [X] |
| P1-5 | SQLite WAL, busy timeout, single writer; store errors fail closed | [X] |
| P1-6 | Postgres is the default affinity store (SQLite still supported) | [X] |
| P1-7 | Config `user_affinity` login hint; `GET /hap/user-affinity`; `POST /hap/users/by-name/{username}/unpin`; `make user-affinity` / `user-unpin` | [X] |

## P2 - Proxy

| ID | Task | Status |
| --- | --- | --- |
| P2-1 | Streaming reverse proxy + WebSocket | [X] |
| P2-2 | Login `AccessToken` peek; logout drops token row only | [X] |
| P2-3 | Honest backend vs HAP errors; no ID rewrite | [X] |
| P2-4 | Header-less media/images follow the session (cookie + IP glue) | [X] |
| P2-5 | Hop ResponseHeaderTimeout / idle / dial timeouts; request-start log | [X] |
| P2-6 | Accept `Expect: 100-continue` before the hop (Delfin/libsoup) | [X] |
| P2-7 | Login failover on hop timeout or 401 (DeviceId pin cannot trap auth) | [X] |
| P2-8 | Optional per-backend in-memory image cache (off by default) | [X] |
| P2-9 | `performance` block: image cache on by default, library JSON cache, coalesce, auth_timeout; optional warm login + library concurrency | [X] |
| P2-10 | Disk-backed image cache (memory hot LRU + optional disk, Compose `hap-cache` volume) | [X] |
| P2-11 | Keep Jellyfin Web on HAP’s origin (no bounce to `backends[].url`) | [ ] |
| P2-12 | Investigate: My Media → Movies / TV shows fail through HAP after home-screen section changes | [X] |

`P2-11` — today `GET /web` hops with `Host` = upstream host and does not rewrite `Location`, so a public HTTPS backend 302s the browser off HAP (`#/home` is the SPA after that). Goal: affinity-chosen server’s player stays on the HAP host; XHR, images, streams, `/socket` stay on HAP (caches apply). Not a merged library; no ID rewrite.

- Forward `X-Forwarded-Host` / proto / port for the host the user typed; do not let “HTTPS required” 301 to the backend name.
- Rewrite `Location` / `Content-Location` to the inbound origin (`ReverseProxy` `Rewrite`+`SetURL`, or equivalent — custom `Director`+`ModifyResponse` does not).
- Hop shape: prefer internal `url` + `Host` = HAP name; if `url` stays the public vanity host, rewrite every absolute 3xx (ingress will keep speaking as that backend).
- Optional: rewrite `/System/Info/Public` `LocalAddress` / WAN fields if Web still bounces (URLs only).
- Watch HLS/DASH absolute segment URLs (bypass HAP). HAP-on-HTTP vs Jellyfin HTTPS-required. Same-backend pin for `/web` assets vs API if versions differ.
- Docs: Jellyfin known-proxies / published URI when HAP is the web entry; `clients.md` Web note.

`P2-12` — not a HAP routing/cache bug. My Media tiles for Movies/TV show placeholders because the pinned backend returns **404** on those two collection folders’ `/Images/Primary` and `/Images/Backdrop` (~50ms). Collections/Anime Primaries are **200** on the same hop. Opening the library (`/Users/…/Items/{id}`) is **200**. Jellyfin Web’s “Failed to load media backdrop” is that Backdrop 404. Home `Views` / `Latest` can still take several seconds (upstream), which feels like a hang; the artwork miss is the 404s. Fix on the Jellyfin side: set library images for those folders (or accept the placeholder).

## P3 - Health

| ID | Task | Status |
| --- | --- | --- |
| P3-1 | Reachability, public info, `/health`, passive auth-plane, optional probe | [X] |
| P3-2 | Status bind + Prometheus; `/hap/users` | [X] |
| P3-3 | Operator backend disable (YAML + store overlay, `POST /hap/backends/{name}/…`, `make backends*`) | [X] |

## Future

| ID | Task | Status |
| --- | --- | --- |
| F-1 | Multi-HAP shared Postgres affinity (no custom cluster protocol in v1) | [ ] |
| F-2 | Explicit ID translation | [ ] |
| F-3 | HTML admin UI | [ ] |
