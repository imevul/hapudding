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

## P2 - Proxy

| ID | Task | Status |
| --- | --- | --- |
| P2-1 | Streaming reverse proxy + WebSocket | [X] |
| P2-2 | Login `AccessToken` peek; logout drops token row only | [X] |
| P2-3 | Honest backend vs HAP errors; no ID rewrite | [X] |

## P3 - Health

| ID | Task | Status |
| --- | --- | --- |
| P3-1 | Reachability, public info, `/health`, passive auth-plane, optional probe | [X] |
| P3-2 | Status bind + Prometheus; `/hap/users` | [X] |

## Future

| ID | Task | Status |
| --- | --- | --- |
| F-1 | Multi-HAP shared Postgres affinity (no custom cluster protocol in v1) | [ ] |
| F-2 | Explicit ID translation | [ ] |
| F-3 | HTML admin UI | [ ] |
