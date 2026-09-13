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
	if err := writeCodexConfig("[::1]:8787", "PROXY_API_KEY"); err != nil {
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

func TestWriteCodexConfigOmitsEnvKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := writeCodexConfig("127.0.0.1:8787", ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "env_key") {
		t.Fatalf("unexpected env_key:\n%s", b)
	}
}

func TestServeRefusesNonLoopbackWithoutKey(t *testing.T) {
	t.Setenv("PROXY_API_KEY", "")
	t.Setenv("GROK_HOME", t.TempDir())
	code := cmdServe([]string{"--host", "0.0.0.0", "--port", "0", "--no-write-config"})
	if code != 2 {
		t.Fatalf("code=%d", code)
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
	t.Setenv("GROK_OAUTH_TOKEN", "")
	t.Setenv("XAI_API_KEY", "")
	p := proxy.Resolve(serveConfig(filepath.Join(t.TempDir(), "auth.json")))
	if p.Upstream != "https://cli-chat-proxy.grok.com" {
		t.Fatalf("upstream=%q", p.Upstream)
	}
}

func TestAPIUpstreamUsesXAIBaseURL(t *testing.T) {
	t.Setenv("XAI_BASE_URL", "https://api.example/v1")
	t.Setenv("GROK_CLI_CHAT_PROXY_BASE_URL", "")
	t.Setenv("GROK_OAUTH_TOKEN", "")
	t.Setenv("XAI_API_KEY", "xai-x")
	p := proxy.Resolve(serveConfig(filepath.Join(t.TempDir(), "auth.json")))
	if p.Upstream != "https://api.example" {
		t.Fatalf("upstream=%q", p.Upstream)
	}
}

func TestOAuthUpstreamUsesCLIBaseURL(t *testing.T) {
	t.Setenv("XAI_BASE_URL", "https://api.example/v1")
	t.Setenv("GROK_CLI_CHAT_PROXY_BASE_URL", "https://cli.example/v1")
	t.Setenv("GROK_OAUTH_TOKEN", "tok")
	t.Setenv("XAI_API_KEY", "")
	p := proxy.Resolve(serveConfig(filepath.Join(t.TempDir(), "auth.json")))
	if p.Upstream != "https://cli.example" {
		t.Fatalf("upstream=%q", p.Upstream)
	}
}
