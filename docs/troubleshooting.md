# Troubleshooting

**Missing auth.json / `run grok login`.** Run `grok login` (or `grok login --device-auth` on a VPS). Then `grok-codex-proxy status`.

**Expired refresh.** If refresh returns HTTP 4xx, the refresh token is dead. `grok login` again.

**502 on `/v1/*` with a `token refresh` body.** The session is still in its skew window but the token endpoint failed. Retry; this is not “run grok login” unless `status` shows a hard-expired session.

**401/403 from the CLI proxy.** The session was rejected. The proxy forces a refresh and retries GET `/v1/models` once. POST is not replayed. If it still 401s, `grok login`, or use `XAI_API_KEY` (API billing, not subscription). This proxy does not guess other hosts.

**413 request too large.** The JSON body exceeded 32MiB. Shrink the request; the proxy does not forward a truncated prefix.

**426 / “Grok CLI version (none) is outdated”.** The CLI proxy requires `x-grok-client-version`. This proxy sends it from `~/.grok/version.json` (or `GROK_CLIENT_VERSION`). Install/update the official `grok` CLI if that file is missing.

**Codex still using OpenAI.** `serve` does not change your default model. Pass `-c model_provider="xai-oauth" -m grok-4.6`. Fully quit and reopen Codex after the first serve. Confirm `~/.codex/config.toml` has `[model_providers.xai-oauth]`.

**Port in use.** `grok-codex-proxy serve --port 8788` and check the `base_url` it wrote.

**Non-loopback refused.** Set `PROXY_API_KEY` or bind `127.0.0.1`.
