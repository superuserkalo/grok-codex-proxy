package proxy_test

import (
	"strings"
	"testing"

	"github.com/superuserkalo/grok-codex-proxy/proxy"
)

func TestUpsertProviderInsertsAndReplacesWithoutTouchingModel(t *testing.T) {
	src := "model = \"gpt-6-astra\"\nmodel_provider = \"openai\"\n\n[notice]\nfast_default_opt_out = true\n"
	got := proxy.UpsertProvider(src, "http://127.0.0.1:8787/v1", "")
	if !strings.Contains(got, "model = \"gpt-6-astra\"") {
		t.Fatalf("model line lost:\n%s", got)
	}
	if !strings.Contains(got, "model_provider = \"openai\"") {
		t.Fatalf("model_provider lost:\n%s", got)
	}
	if !strings.Contains(got, "[model_providers.xai-oauth]") || !strings.Contains(got, `base_url = "http://127.0.0.1:8787/v1"`) {
		t.Fatalf("provider missing:\n%s", got)
	}
	if strings.Contains(got, "env_key") {
		t.Fatalf("unexpected env_key:\n%s", got)
	}

	got2 := proxy.UpsertProvider(got, "http://127.0.0.1:9999/v1", "GROK_CODEX_PROXY_KEY")
	if count := strings.Count(got2, "[model_providers.xai-oauth]"); count != 1 {
		t.Fatalf("tables=%d\n%s", count, got2)
	}
	if !strings.Contains(got2, `base_url = "http://127.0.0.1:9999/v1"`) {
		t.Fatalf("base_url not replaced:\n%s", got2)
	}
	if strings.Contains(got2, "8787") {
		t.Fatalf("old port lingered:\n%s", got2)
	}
	if !strings.Contains(got2, `env_key = "GROK_CODEX_PROXY_KEY"`) {
		t.Fatalf("env_key missing:\n%s", got2)
	}
	if !strings.Contains(got2, "model = \"gpt-6-astra\"") {
		t.Fatalf("model clobbered:\n%s", got2)
	}
}
