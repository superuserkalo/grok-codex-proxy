# grok-codex-proxy

Go stdlib loopback proxy. Codex talks to `127.0.0.1:8787/v1`; this process forwards to the official Grok CLI chat proxy using `~/.grok/auth.json`.

## Layout

| File | What |
| --- | --- |
| `main.go` | `status` / `serve` |
| `auth.go` | auth.json load, refresh, atomic save |
| `codex.go` | surgical `[model_providers.xai-oauth]` upsert |
| `forward.go` | model slug + SSE copy |
| `server.go` | mux, gate token, upstream |
| `slug.go` | `MapModel` |
| `docs/design.md` | spec |

## Commands

```bash
go test ./...
go build -o grok-codex-proxy .
```

## Constraints

- Stdlib only. No extra HTTP client, no TOML library, no CLI framework.
- Never log tokens or `auth.json`. Never copy `~/.grok/auth.json` into the repo.
- Do not add `login`, images, video, TTS, telemetry, or a protocol translator unless a live Codex run proves a mismatch.
- `serve` upserts the Codex provider table only. Do not change top-level `model` / `model_provider`.
- YAGNI. If it is not in `docs/design.md`, do not build it.
