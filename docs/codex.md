# Codex

`grok-codex-proxy serve` writes this table into `${CODEX_HOME:-$HOME/.codex}/config.toml` (user-level only; project configs cannot set `model_providers`):

```toml
[model_providers.xai-oauth]
name = "xAI Grok OAuth (local)"
base_url = "http://127.0.0.1:8787/v1"
wire_api = "responses"
```

If `PROXY_API_KEY` is set, it also writes `env_key = "GROK_CODEX_PROXY_KEY"`. Export that to the same value:

```bash
export PROXY_API_KEY=...
export GROK_CODEX_PROXY_KEY="$PROXY_API_KEY"
```

The proxy does not change top-level `model` or `model_provider`. Keep your existing default. Invoke Grok with:

```bash
codex -c model_provider="xai-oauth" -m grok-4.6 "reply with the word pong"
```

Skip the write with `grok-codex-proxy serve --no-write-config`.

Fully quit and reopen Codex after a config write. Start `serve` before Codex.
