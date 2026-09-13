# Auth

Path: `${GROK_HOME:-$HOME/.grok}/auth.json`. Written by `grok login`. This proxy only reads and refreshes it.

## Shape

JSON object keyed by `issuer::client_id`. Pick order: official `https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828`, else first sorted key prefixed `https://auth.x.ai`, else first sorted entry whose `oidc_issuer` contains `auth.x.ai`.

Access token: `key`, else `access_token`. Also `refresh_token`, `expires_at`, plus profile fields that are preserved on write.

## Refresh

About 300 seconds before `expires_at`, POST `https://auth.x.ai/oauth2/token` with `grant_type=refresh_token` and the public Grok CLI client id. Atomic write (temp + rename), mode `0600`. Happens before the upstream call so a live SSE is not killed.

## Credential order

1. Valid OAuth file entry → `https://cli-chat-proxy.grok.com/v1` with CLI headers (`X-XAI-Token-Auth`, `x-grok-model-override`, `x-grok-client-version` from `~/.grok/version.json`)
2. `GROK_OAUTH_TOKEN` → same host and headers
3. `XAI_API_KEY` → `https://api.x.ai/v1`, Bearer only

`XAI_BASE_URL` overrides the upstream base. `GROK_CLI_CHAT_PROXY_BASE_URL` overrides the OAuth host only.

If the file exists but cannot yield a token, `serve` returns 401 (`run grok login`) and `status` exits nonzero. Neither falls through to an API key. `status` and `serve` use this same cascade, including which upstream and headers a request gets.

There is no `proxy login`. Use `grok login` or `grok login --device-auth`.
