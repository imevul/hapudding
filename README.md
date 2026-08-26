# HAP — Highly Available Pudding

Jellyfin-aware reverse proxy for a pool of **one or more** independent Jellyfin instances.

Each Jellyfin is its own database. Server IDs, user IDs, item IDs, and auth tokens are backend-local. HAP routes with **token and DeviceId affinity**. It does not rewrite IDs unless `translate.server_id` is enabled, and then only `/System/Info*` `Id` (optional `name` may also replace `ServerName`). It does not pretend two servers are one.

Point your existing ingress at **one HAP target**, not at the Jellyfin servers directly.

Source: [github.com/imevul/hapudding](https://github.com/imevul/hapudding). Official binary: **`hapudding`**. Alias: **`hap`**.

## Config

```bash
cp .env.example .env
cp configs/hap.example.yaml configs/hap.yaml
```

Edit `configs/hap.yaml` and add your Jellyfin instances under `backends` (`name` + `url`, plus optional hop `host` / `headers` / `tls`). Use URLs the HAP process can reach (from Docker that is not `127.0.0.1` on the host unless the backend is in the same compose network). Env in `.env` wins over YAML; leave variables commented until you need them. See [docs/config.md](docs/config.md).

## Install

Official binary: **`hapudding`**. Convenience alias: **`hap`** (symlink).

```bash
make build          # bin/hapudding and bin/hap
make verify
bin/hapudding --config configs/hap.yaml
```

Docker:

```bash
make compose-up                           # docker compose up --build (HAP + Postgres)
# or: docker compose up --build
```

The image runs `hapudding` as uid 65532. Compose starts as root only long enough to `chown /data`, then drops privileges. Compose mounts `configs/hap.yaml` at `/config/hap.yaml`.

## Update

```bash
make update
# docker only (no git pull): make compose-up
```

`git pull --ff-only` when an upstream is set, then `go mod tidy` and rebuild `bin/hapudding`. If the Compose `hap` service is already running, it is rebuilt and restarted. Restart a host `hapudding` process yourself. To bring Docker up without updating git, use `make compose-up`. Host `make run` expects Postgres at `localhost:5432` (Compose publishes `5432`).

See [docs/README.md](docs/README.md) for config, affinity policies, health, and client notes.

## License

MIT. See [LICENSE](LICENSE).
