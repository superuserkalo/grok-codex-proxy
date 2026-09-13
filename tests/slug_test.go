package proxy_test

import (
	"testing"

	"github.com/superuserkalo/grok-codex-proxy/proxy"
)

func TestMapModel(t *testing.T) {
	cases := map[string]string{
		"grok-4.6":            "grok-4.6",
		"xai/grok-4.6":        "grok-4.6",
		"grok-oauth/grok-4.6": "grok-4.6",
		"grok-4.5":            "grok-4.5",
		"xai/grok-4.5":        "grok-4.5",
		"grok-oauth/grok-4.5": "grok-4.5",
		"grok-4-fast":         "grok-4-fast",
		"xai/custom-thing":    "custom-thing",
	}
	for in, want := range cases {
		if got := proxy.MapModel(in); got != want {
			t.Fatalf("MapModel(%q)=%q want %q", in, got, want)
		}
	}
}
