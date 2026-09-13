# grok-codex-proxy

Local OpenAI-compatible proxy so [OpenAI Codex](https://github.com/openai/codex) can use your existing Grok CLI OAuth session (SuperGrok / X Premium).

This is **not** an official OpenAI or xAI product. It is a small loopback shim: it reads the official Grok CLI session at `~/.grok/auth.json` and forwards `/v1` to xAI’s official CLI chat proxy. It is not a coding agent and not a community router.

## Install

```bash
go install github.com/superuserkalo/grok-codex-proxy@latest
```

Or from this repo:

```bash
go build -o grok-codex-proxy .
```

## Use

```bash
grok login                          # official CLI; writes ~/.grok/auth.json
grok-codex-proxy status
grok-codex-proxy serve              # 127.0.0.1:8787
```

`serve` upserts `[model_providers.xai-oauth]` in `~/.codex/config.toml`. It does **not** change your default Codex `model`. Fully quit and reopen Codex after the first serve.

Then:

```bash
codex -c model_provider="xai-oauth" -m grok-4.6 "reply with the word pong"
```

Headless / SSH: `grok login --device-auth` (alias `--device-code`).

## Billing

OAuth / SuperGrok traffic goes through `https://cli-chat-proxy.grok.com/v1` and counts against your Grok subscription. `XAI_API_KEY` goes to `https://api.x.ai/v1` and is billed as API usage. Those are different.

## Security

- Binds `127.0.0.1` by default. Non-loopback bind is refused unless `PROXY_API_KEY` is set.
- Never logs access tokens, refresh tokens, or `auth.json`.
- Writes `auth.json` and new Codex config files with mode `0600`.
- Does not copy `~/.grok/auth.json` anywhere.

## Verify

```bash
grok login && grok-codex-proxy status
grok-codex-proxy serve
curl http://127.0.0.1:8787/v1/models
curl -s -N http://127.0.0.1:8787/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"grok-4.6","input":"reply with the word pong","stream":true}'
codex -c model_provider="xai-oauth" -m grok-4.6 "reply with the word pong"
```

## Docs

- [Codex config](docs/codex.md)
- [Auth](docs/auth.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Design](docs/design.md)

MIT licensed.
