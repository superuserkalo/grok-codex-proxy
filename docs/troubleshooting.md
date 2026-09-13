# Troubleshooting

**Missing auth.json / `run grok login`.** Run `grok login` (or `grok login --device-auth` on a VPS). Then `grok-codex-proxy status`.

**Expired refresh.** If refresh returns HTTP 4xx, the refresh token is dead. `grok login` again.

**401/403 from the CLI proxy.** The session is not accepted. `grok login`, or use `XAI_API_KEY` (API billing, not subscription). This proxy does not guess other hosts.

**426 / “Grok CLI version (none) is outdated”.** The CLI proxy requires `x-grok-client-version`. This proxy sends it from `~/.grok/version.json` (or `GROK_CLIENT_VERSION`). Install/update the official `grok` CLI if that file is missing.

**Codex still using OpenAI.** `serve` does not change your default model. Pass `-c model_provider="xai-oauth" -m grok-4.6`. Fully quit and reopen Codex after the first serve. Confirm `~/.codex/config.toml` has `[model_providers.xai-oauth]`.

**Port in use.** `grok-codex-proxy serve --port 8788` and check the `base_url` it wrote.

**Non-loopback refused.** Set `PROXY_API_KEY` (and `GROK_CODEX_PROXY_KEY` to the same value) or bind `127.0.0.1`.
