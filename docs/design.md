# grok-codex-proxy

Local OpenAI-compatible proxy so Codex can use an existing Grok CLI OAuth session (SuperGrok / X Premium). Not an official OpenAI or xAI product. Not a coding agent. Not a community router.

## Locked decisions

- Language: Go, stdlib only (`flag`, `net/http`, `encoding/json`). No Cobra, no TOML library, no extra HTTP client.
- Binary: `grok-codex-proxy`. Commands: `status`, `serve`. No `login` — use `grok login` / `grok login --device-auth`.
- OAuth inference host: `https://cli-chat-proxy.grok.com/v1`. Override with `GROK_CLI_CHAT_PROXY_BASE_URL`. API keys use `https://api.x.ai/v1`. `XAI_BASE_URL` overrides whichever path is active.
- Thin authenticated forward of `/v1/models`, `/v1/responses`, `/v1/chat/completions`. Rewrite model slug + inject headers. No protocol translator unless a live Codex run shows a mismatch.
- `serve` surgically upserts `[model_providers.xai-oauth]` in `~/.codex/config.toml`. It does not change top-level `model` or `model_provider`.
- Refresh 300s before `expires_at` (same as Grok CLI `GROK_AUTH_EARLY_INVALIDATION_SECS`).
- Default bind `127.0.0.1:8787`. Optional `PROXY_API_KEY`. No telemetry. No images/video/TTS.

## Architecture

```
Codex  →  127.0.0.1:8787/v1/*  →  cli-chat-proxy.grok.com/v1/*
                 ↑
         ~/.grok/auth.json   (read + refresh only)
```

One process. Auth shim + header injector.

### Layout

```
main.go      status / serve
auth.go      load auth.json, pick entry, refresh, atomic write
server.go    bind, optional gate token, /healthz, config upsert
forward.go   slug map, CLI headers, stream copy
*_test.go    beside the code they cover
```

## Auth

Path: `${GROK_HOME:-$HOME/.grok}/auth.json`.

Official shape: JSON object keyed by `issuer::client_id`. Prefer `https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828`. Access token is `key`, else `access_token`. Also: `refresh_token`, `expires_at`, `oidc_issuer`, `oidc_client_id`, `auth_mode`, plus profile fields that must be preserved on write.

Refresh: POST `https://auth.x.ai/oauth2/token` with `grant_type=refresh_token`, public client `b1a00492-073a-47ea-816f-4c329264a828`, no secret. Update `key`, rotated `refresh_token` if present, `expires_at`. Keep unknown fields. Write via temp file in the same directory + `rename`, mode `0600`. Refresh before the upstream call so an in-flight SSE is not killed.

Credential order:

1. Valid OAuth file entry (cli-chat-proxy + CLI headers)
2. `GROK_OAUTH_TOKEN` (same host and CLI headers as the file; session token, not an API key)
3. `XAI_API_KEY` (`api.x.ai`, Bearer only, no CLI headers)

Missing/unrefreshable session: `status` exits nonzero; `serve` returns 401 with “run `grok login`”.

Never log access tokens, refresh tokens, or raw `auth.json`. Never copy `auth.json` into this repo.

## HTTP

| Method | Path | Behavior |
| --- | --- | --- |
| GET | `/healthz` | local `ok` |
| GET | `/v1/models` | forward |
| POST | `/v1/responses` | forward, SSE if `stream` |
| POST | `/v1/chat/completions` | forward, SSE if `stream` |

OAuth upstream headers:

- `Authorization: Bearer <access_token>`
- `X-XAI-Token-Auth: xai-grok-cli`
- `x-grok-model-override: <mapped model>`

Model slugs: `grok-4.6`, `xai/grok-4.6`, `grok-oauth/grok-4.6` → `grok-4.6`. Same for `grok-4.5`. Unknown slugs pass through.

Stream the response with `Flush`. Do not parse tool-call JSON. Bodies pass through except the model slug.

Retries: only idempotent calls (GET `/v1/models`) on 429 / transient 5xx. Never retry POST streams.

Bind: `--host` (default `127.0.0.1`), `--port` (default `8787`). If `PROXY_API_KEY` is set, require `Authorization: Bearer $PROXY_API_KEY` from Codex, then swap in the upstream token. If unset, loopback only. Refuse to start if bound off-loopback without `PROXY_API_KEY`.

Upstream 401/403 on OAuth: return that status plus “token rejected by the CLI proxy; run `grok login` or use `XAI_API_KEY`”. No host guessing.

Logs: method, path, status, duration, mapped model. No tokens.

## Codex config

On `serve` (unless `--no-write-config`): upsert this table in `${CODEX_HOME:-$HOME/.codex}/config.toml`:

```toml
[model_providers.xai-oauth]
name = "xAI Grok OAuth (local)"
base_url = "http://127.0.0.1:8787/v1"
wire_api = "responses"
```

`base_url` uses the actual listen host/port. Add `env_key = "GROK_CODEX_PROXY_KEY"` only when `PROXY_API_KEY` is set.

Surgical text upsert, stdlib only:

- If `[model_providers.xai-oauth]` exists, replace that table until the next `[` header or EOF.
- Else append the table.
- Do not modify `model`, `model_provider`, or any other key.
- Do not round-trip the whole file through a TOML encoder (comments, order, and unrelated tables must survive).
- Atomic write. Create the file at `0600` if missing.

Codex still uses the user’s existing default model. Invoke Grok with:

```bash
codex -c model_provider="xai-oauth" -m grok-4.6 "reply with the word pong"
```

Fully quit and reopen Codex after a config write. Project configs cannot set `model_providers`.

## CLI

```
grok-codex-proxy status
grok-codex-proxy serve [--host 127.0.0.1] [--port 8787] [--no-write-config]
```

`status`: auth path, chosen entry, expiry, whether refresh is due, upstream base. No token values.

## Tests

- `auth.json` parse: keyed entry, `key` vs `access_token`, preserve unknown fields
- refresh schedule: due inside 300s, not due outside
- model slug mapping
- SSE copy smoke (chunked `data:` lines survive)
- config upsert: insert, replace, leave `model` / other tables alone, no duplicate table

## Docs in the repo (implementation)

- `README.md` — what this is / is not, install, `grok login`, `status`, `serve`, Codex invoke, billing, security, verify
- `docs/codex.md` — provider table, `-c model_provider`, restart rule
- `docs/auth.md` — `auth.json`, refresh, credential order
- `docs/troubleshooting.md` — missing auth, expired refresh, 401/403, Codex still on OpenAI, port in use
- `AGENTS.md` — build/test, privacy, YAGNI
- `CLAUDE.md` — pointer to `AGENTS.md`
- `.env.example` — `GROK_HOME`, `PROXY_API_KEY`, `XAI_BASE_URL`, `GROK_CLI_CHAT_PROXY_BASE_URL` (no secrets)
- MIT license

## Out of scope

- `proxy login` / PKCE / device code
- Images, video, TTS
- Chat↔Responses tool-call translation
- Telemetry
- Community routers (OpenCodex, Codex Router, model-router)
- Changing the user’s default Codex model

## Verify

1. `grok login && grok-codex-proxy status`
2. `grok-codex-proxy serve`
3. `curl http://127.0.0.1:8787/v1/models`
4. Tiny streamed `POST /v1/responses` for `grok-4.6`
5. `codex -c model_provider="xai-oauth" -m grok-4.6 "reply with the word pong"`
