# AGENTS.md — HAP (Highly Available Pudding)

Instructions for coding agents working in this repository.

## Project

- **Public name:** HAP — Highly Available Pudding
- **Official binary / artifacts:** `hapudding` (`cmd/hapudding`)
- **Alias:** `hap` (symlink to the same binary)
- **Go module / GitHub:** `github.com/imevul/hapudding`
- **License:** MIT
- **Versions:** SemVer git tags `vMAJOR.MINOR.PATCH`

## Boundaries

- HAP is a reverse proxy. It does not rewrite Jellyfin Server/User/Item IDs unless `translate.server_id` is enabled, and then only `/System/Info*` `Id` (optional `name` may also replace `ServerName`).
- Do not send an authenticated token issued by backend A to backend B.
- Gray-list is header/path/store client class (default Infuse), not a Jellyfin user identity.
- Do not put personal, secret, or deployment-specific information in the repo.
- Runtime secrets live in gitignored `.env` and `data/`.
- Committed files (docs, examples, tests, comments, Make help) must use only generic backend names (`server-a`, `server-b`) and fictional URLs (`127.0.0.1`, `example.test`). Never commit real operator hostnames, site names, or other deployment identifiers.

## Layout

- Docs: [`docs/`](docs/) (`SPEC.md`, `ROADMAP.md`, `config.md`, `affinity.md`, `health.md`, `clients.md`)
- Config example: [`configs/hap.example.yaml`](configs/hap.example.yaml)
- Code: `cmd/hapudding`, `internal/{authheader,config,store,router,health,proxy,imgcache,libcache,status}` (status HTML under `internal/status/ui/`)

## Verification

```bash
make verify              # fmt-check tidy-check vet test build
make ci                  # plus test-race
make test test-race
make update              # git pull --ff-only, tidy, build; rebuild running Compose
```

When a change ships, splits, or reschedules a roadmap line, update [`docs/ROADMAP.md`](docs/ROADMAP.md) in the same change.
