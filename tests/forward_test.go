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

func TestRewriteModelPreservesNonModelBytes(t *testing.T) {
	body := []byte(`{"model":"xai/grok-4.6","max_tokens":9007199254740993,"temperature":0.0,"tools":[{"function":{"parameters":{"n":1}}}]}`)
	out, model := proxy.RewriteModel(body)
	if model != "grok-4.6" {
		t.Fatalf("model=%q", model)
	}
	if !bytes.Contains(out, []byte(`"model":"grok-4.6"`)) {
		t.Fatalf("body=%s", out)
	}
	if !bytes.Contains(out, []byte(`9007199254740993`)) {
		t.Fatalf("integer corrupted: %s", out)
	}
	if !bytes.Contains(out, []byte(`0.0`)) {
		t.Fatalf("float rewritten: %s", out)
	}
	if !bytes.Contains(out, []byte(`{"n":1}`)) {
		t.Fatalf("nested json rewritten: %s", out)
	}
}

func TestRewriteModelUnchangedSlugKeepsOriginalBody(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","max_tokens":9007199254740993}`)
	out, model := proxy.RewriteModel(body)
	if model != "grok-4.6" {
		t.Fatalf("model=%q", model)
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("rewrote untouched slug: %s", out)
	}
}

func TestRewriteModelLeavesOtherBytesIntact(t *testing.T) {
	body := []byte(`{"z":1, "model": "xai/grok-4.6", "a":2}`)
	out, model := proxy.RewriteModel(body)
	if model != "grok-4.6" {
		t.Fatalf("model=%q", model)
	}
	want := []byte(`{"z":1, "model": "grok-4.6", "a":2}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("got %s want %s", out, want)
	}
}

func TestRewriteModelDoesNotTouchSlugInOtherFields(t *testing.T) {
	body := []byte(`{"input":"xai/grok-4.6","model":"xai/grok-4.6"}`)
	out, model := proxy.RewriteModel(body)
	if model != "grok-4.6" {
		t.Fatalf("model=%q", model)
	}
	want := []byte(`{"input":"xai/grok-4.6","model":"grok-4.6"}`)
	if !bytes.Equal(out, want) {
		t.Fatalf("got %s want %s", out, want)
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
