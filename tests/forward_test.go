package proxy_test

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/superuserkalo/grok-codex-proxy/proxy"
)

func TestRewriteModel(t *testing.T) {
	out, model := proxy.RewriteModel([]byte(`{"model":"xai/grok-4.6","stream":true,"input":"hi"}`))
	if model != "grok-4.6" {
		t.Fatalf("model=%q", model)
	}
	if !bytes.Contains(out, []byte(`"model":"grok-4.6"`)) && !bytes.Contains(out, []byte(`"model": "grok-4.6"`)) {
		t.Fatalf("body=%s", out)
	}
	if !bytes.Contains(out, []byte(`"stream"`)) {
		t.Fatalf("dropped fields: %s", out)
	}
}

func TestJoinURLStripsDuplicateV1(t *testing.T) {
	cases := map[[2]string]string{
		{"https://cli-chat-proxy.grok.com/v1", "/v1/responses"}: "https://cli-chat-proxy.grok.com/v1/responses",
		{"https://cli-chat-proxy.grok.com/v1", "/v1/models"}:    "https://cli-chat-proxy.grok.com/v1/models",
		{"https://api.x.ai/v1", "/v1/chat/completions"}:         "https://api.x.ai/v1/chat/completions",
		{"http://127.0.0.1:1234", "/v1/responses"}:              "http://127.0.0.1:1234/v1/responses",
	}
	for in, want := range cases {
		if got := proxy.JoinURL(in[0], in[1]); got != want {
			t.Fatalf("JoinURL(%q,%q)=%q want %q", in[0], in[1], got, want)
		}
	}
}

func TestCopyStreamWritesSSEChunks(t *testing.T) {
	src := strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"p\"}\n\ndata: [DONE]\n\n")
	rr := httptest.NewRecorder()
	proxy.CopyStream(rr, src)
	got := rr.Body.String()
	if !strings.Contains(got, "response.output_text.delta") || !strings.Contains(got, "[DONE]") {
		t.Fatalf("sse=%q", got)
	}
}
