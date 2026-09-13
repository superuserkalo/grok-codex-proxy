package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthzAndForwardInjectsCLIHeaders(t *testing.T) {
	var sawAuth, sawCLI, sawOverride, path string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		sawAuth = r.Header.Get("Authorization")
		sawCLI = r.Header.Get("X-XAI-Token-Auth")
		sawOverride = r.Header.Get("x-grok-model-override")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
	}))
	t.Cleanup(up.Close)

	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	body := `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "session-token",
	    "expires_at": "2099-01-01T00:00:00Z"
	  }
	}`
	if err := os.WriteFile(auth, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := NewMux(Config{
		Upstream:      up.URL,
		UseCLIHeaders: true,
		AuthPath:      auth,
		HTTPClient:    up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || strings.TrimSpace(string(b)) != "ok" {
		t.Fatalf("healthz %d %s", res.StatusCode, b)
	}

	req, _ := http.NewRequest("POST", srv.URL+"/v1/responses", strings.NewReader(`{"model":"xai/grok-4.6","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if path != "/v1/responses" {
		t.Fatalf("path=%q", path)
	}
	if sawAuth != "Bearer session-token" || sawCLI != "xai-grok-cli" || sawOverride != "grok-4.6" {
		t.Fatalf("headers auth=%q cli=%q ov=%q", sawAuth, sawCLI, sawOverride)
	}
	if !strings.Contains(string(out), "response.created") {
		t.Fatalf("sse=%s", out)
	}
}

func TestProxyAPIKeyGate(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)
	mux := NewMux(Config{Upstream: up.URL, ProxyAPIKey: "secret", APIKey: "xai-x", HTTPClient: up.Client()})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	res.Body.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	res.Body.Close()
}

func TestLoopbackHost(t *testing.T) {
	if !loopbackHost("127.0.0.1") || !loopbackHost("localhost") || !loopbackHost("::1") {
		t.Fatal("loopback")
	}
	if loopbackHost("0.0.0.0") || loopbackHost("1.2.3.4") {
		t.Fatal("non-loopback")
	}
}
