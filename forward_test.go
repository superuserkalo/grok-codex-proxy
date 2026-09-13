package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRewriteModel(t *testing.T) {
	out, model := rewriteModel([]byte(`{"model":"xai/grok-4.6","stream":true,"input":"hi"}`))
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

func TestCopyStreamWritesSSEChunks(t *testing.T) {
	src := strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"p\"}\n\ndata: [DONE]\n\n")
	rr := httptest.NewRecorder()
	copyStream(rr, src)
	got := rr.Body.String()
	if !strings.Contains(got, "response.output_text.delta") || !strings.Contains(got, "[DONE]") {
		t.Fatalf("sse=%q", got)
	}
}
