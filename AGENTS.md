# grok-codex-proxy

Go stdlib loopback proxy. Codex talks to `127.0.0.1:8787/v1`; this process forwards to the official Grok CLI chat proxy using `~/.grok/auth.json`.

README is for humans. This file is for agents.

## Layout

| Path | What |
| --- | --- |
| `main.go` | `status` / `serve` |
| `proxy/auth.go` | auth.json load, refresh, atomic save, credential pick, serialized session |
| `proxy/session_test.go` | Store cache vs `auth.json` mtime |
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
- Thin forward: rewrite only the model slug (leave other JSON bytes intact), inject upstream headers, pass the body through, `Flush` SSE. Do not parse tool-call JSON.
- One credential cascade for `status` and `serve` (file, `GROK_OAUTH_TOKEN`, `XAI_API_KEY`). `Pick.Source` is that cascade; `Pick.CLI` is the header dialect. `Pick.Token` is the bearer for this request. `Pick.gate` is 401 vs 502 (502 = the store still has a live token we did not send).
- Serialize refresh in `session` under the mutex. Cache the Store against `auth.json` mtime. CLI 401 force-refresh is session state under the same lock and only runs when a Store exists. The token HTTP call uses a background 15s timeout, not the inbound request context. A rotated session stays in process if `auth.json` save fails, until the file mtime is newer.
- No creds / hard-expired session → 401. In-window refresh transport failure → 502. Body over 32MiB → 413.
- Retry GET `/v1/models` on 429 / transient 5xx, and once after a CLI 401/403 rotation. Never replay POST streams.
- Default bind `127.0.0.1:8787`. Refuse off-loopback unless `PROXY_API_KEY` is set.
- `serve` upserts `[model_providers.xai-oauth]` only: replace that table until the next `[` header, or append. No TOML encoder round-trip. Do not change top-level `model` / `model_provider`.
- Do not add `login`, images, video, TTS, telemetry, or a protocol translator unless a live Codex run proves a mismatch.
- YAGNI. Do not build past the commands and routes above.
