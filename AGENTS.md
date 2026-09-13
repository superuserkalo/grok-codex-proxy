# grok-codex-proxy

Go stdlib loopback proxy. Codex talks to `127.0.0.1:8787/v1`; this process forwards to the official Grok CLI chat proxy using `~/.grok/auth.json`.

README is for humans. This file is for agents.

## Layout

| Path | What |
| --- | --- |
| `main.go` | `status` / `serve` |
| `proxy/auth.go` | auth.json load, refresh, atomic save |
| `proxy/codex.go` | surgical `[model_providers.xai-oauth]` upsert |
| `proxy/forward.go` | model slug + SSE copy |
| `proxy/server.go` | mux, gate token, upstream |
| `proxy/slug.go` | `MapModel` |
| `tests/` | `package proxy_test` |
| `docs/auth.md` | auth.json, refresh, credential order |
| `docs/codex.md` | provider table, `-c model_provider` |
| `docs/troubleshooting.md` | 401/403, Codex still on OpenAI |

## Commands

```bash
go test ./...
go build -o grok-codex-proxy .
```

CI (`.github/workflows/test.yml`): `gofmt`, `go vet`, `go test ./...`. Do not add linters, coverage, or an OS matrix.

## Constraints

- Stdlib only. No extra HTTP client, no TOML library, no CLI framework.
- Never log tokens or `auth.json`. Never copy `~/.grok/auth.json` into the repo.
- Commands: `status`, `serve` (`--host`, `--port`, `--no-write-config`). Routes: GET `/healthz`, GET `/v1/models`, POST `/v1/responses`, POST `/v1/chat/completions`.
- Thin forward: rewrite the model slug, inject upstream headers, pass the body through, `Flush` SSE. Do not parse tool-call JSON.
- Retry only GET `/v1/models` on 429 / transient 5xx. Never retry POST streams.
- Default bind `127.0.0.1:8787`. Refuse off-loopback unless `PROXY_API_KEY` is set.
- `serve` upserts `[model_providers.xai-oauth]` only: replace that table until the next `[` header, or append. No TOML encoder round-trip. Do not change top-level `model` / `model_provider`.
- Do not add `login`, images, video, TTS, telemetry, or a protocol translator unless a live Codex run proves a mismatch.
- YAGNI. Do not build past the commands and routes above.
