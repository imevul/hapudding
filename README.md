# HAP — Highly Available Pudding

Jellyfin-aware reverse proxy for a pool of **one or more** independent Jellyfin instances.

Each Jellyfin is its own database. Server IDs, user IDs, item IDs, and auth tokens are backend-local. HAP routes with **token and DeviceId affinity**. It does not rewrite IDs or pretend two servers are one.

Point your existing ingress at **one HAP target**, not at the Jellyfin servers directly.

Source: [github.com/imevul/hapudding](https://github.com/imevul/hapudding). Official binary: **`hapudding`**. Alias: **`hap`**.

## Install

Official binary: **`hapudding`**. Convenience alias: **`hap`** (symlink).

```bash
make build          # bin/hapudding and bin/hap
make verify
bin/hapudding --config configs/hap.example.yaml
```

Docker:

```bash
docker compose up --build                 # SQLite
docker compose --profile postgres up --build
```

See [docs/README.md](docs/README.md) for config, affinity policies, health, and client notes.

## License

MIT. See [LICENSE](LICENSE).
