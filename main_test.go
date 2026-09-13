package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
