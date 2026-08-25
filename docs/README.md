# Documentation

Operator reference. The root [README](../README.md) is the quick entry point.

| Document | Contents |
| -------- | -------- |
| [SPEC](SPEC.md) | Product constraints, non-goals |
| [ROADMAP](ROADMAP.md) | Phases and task status |
| [config](config.md) | YAML schema, env, hop headers/TLS |
| [affinity](affinity.md) | Lookup order, policies, auth flow, logout |
| [health](health.md) | Layered probes vs proxied backend errors |
| [clients](clients.md) | Infuse, Delfin, CLIamp, WebSocket `api_key` |

Status bind (`status.listen`) is JSON + Prometheus, not an HTML UI. See [SPEC](SPEC.md#status-bind).
