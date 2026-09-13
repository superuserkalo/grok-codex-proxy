package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/superuserkalo/grok-codex-proxy/proxy"
)

func TestWriteCodexConfigIPv6AndProxyAPIKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := writeCodexConfig("[::1]:8787", true); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, `base_url = "http://[::1]:8787/v1"`) {
		t.Fatalf("base_url:\n%s", got)
	}
	if !strings.Contains(got, `env_key = "PROXY_API_KEY"`) {
		t.Fatalf("env_key:\n%s", got)
	}
	if strings.Contains(got, "GROK_CODEX_PROXY_KEY") {
		t.Fatalf("old env name:\n%s", got)
	}
}

func TestStatusUnknownExpiryUsesNeedsRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GROK_HOME", home)
	t.Setenv("GROK_OAUTH_TOKEN", "")
	t.Setenv("XAI_API_KEY", "")
	auth := filepath.Join(home, "auth.json")
	body := `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "session-token"
	  }
	}`
	if err := os.WriteFile(auth, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := cmdStatus()
	w.Close()
	os.Stdout = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	got := string(out)
	if !strings.Contains(got, "expires_at=unknown") {
		t.Fatalf("missing unknown expiry:\n%s", got)
	}
	if !strings.Contains(got, "needs_refresh=false") {
		t.Fatalf("status should use NeedsRefresh:\n%s", got)
	}
}

func TestOAuthUpstreamIgnoresXAIBaseURL(t *testing.T) {
	t.Setenv("XAI_BASE_URL", "https://api.example/v1")
	t.Setenv("GROK_CLI_CHAT_PROXY_BASE_URL", "")
	if got := oauthUpstream(); got != proxy.DefaultOAuthUpstream {
		t.Fatalf("oauthUpstream=%q", got)
	}
}

func TestAPIUpstreamUsesXAIBaseURL(t *testing.T) {
	t.Setenv("XAI_BASE_URL", "https://api.example/v1")
	if got := apiUpstream(); got != "https://api.example/v1" {
		t.Fatalf("apiUpstream=%q", got)
	}
}

func TestOAuthUpstreamUsesCLIBaseURL(t *testing.T) {
	t.Setenv("XAI_BASE_URL", "https://api.example/v1")
	t.Setenv("GROK_CLI_CHAT_PROXY_BASE_URL", "https://cli.example/v1")
	if got := oauthUpstream(); got != "https://cli.example/v1" {
		t.Fatalf("oauthUpstream=%q", got)
	}
}
