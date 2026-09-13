package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/superuserkalo/grok-codex-proxy/proxy"
)

func writeAuth(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHealthzAndForwardInjectsCLIHeaders(t *testing.T) {
	var sawAuth, sawCLI, sawOverride, sawVer, path string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		sawAuth = r.Header.Get("Authorization")
		sawCLI = r.Header.Get("X-XAI-Token-Auth")
		sawOverride = r.Header.Get("x-grok-model-override")
		sawVer = r.Header.Get("x-grok-client-version")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n\n")
	}))
	t.Cleanup(up.Close)

	auth := writeAuth(t, t.TempDir(), `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "session-token",
	    "expires_at": "2099-01-01T00:00:00Z"
	  }
	}`)

	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: up.URL,
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
	if sawAuth != "Bearer session-token" || sawCLI != "xai-grok-cli" || sawOverride != "grok-4.6" || sawVer != "1.0.30" {
		t.Fatalf("headers auth=%q cli=%q ov=%q ver=%q", sawAuth, sawCLI, sawOverride, sawVer)
	}
	if !strings.Contains(string(out), "response.created") {
		t.Fatalf("sse=%s", out)
	}
}

func TestAPIKeySkipsCLIHeaders(t *testing.T) {
	var sawAuth, sawCLI string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawCLI = r.Header.Get("X-XAI-Token-Auth")
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	mux := proxy.NewMux(proxy.Config{
		APIUpstream: up.URL,
		APIKey:      "xai-x",
		AuthPath:    filepath.Join(t.TempDir(), "auth.json"),
		HTTPClient:  up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if sawAuth != "Bearer xai-x" || sawCLI != "" {
		t.Fatalf("auth=%q cli=%q", sawAuth, sawCLI)
	}
}

func TestFileAppearsSwitchesOffAPIKey(t *testing.T) {
	var mu sync.Mutex
	var hosts []string
	var auths []string
	var clis []string
	hit := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		auths = append(auths, r.Header.Get("Authorization"))
		clis = append(clis, r.Header.Get("X-XAI-Token-Auth"))
		mu.Unlock()
		w.WriteHeader(200)
	}
	api := httptest.NewServer(http.HandlerFunc(hit))
	t.Cleanup(api.Close)
	cli := httptest.NewServer(http.HandlerFunc(hit))
	t.Cleanup(cli.Close)

	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: cli.URL,
		APIUpstream:   api.URL,
		APIKey:        "xai-x",
		AuthPath:      auth,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}

	if err := os.WriteFile(auth, []byte(`{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "session-token",
	    "expires_at": "2099-01-01T00:00:00Z"
	  }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err = http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 {
		t.Fatalf("hits=%d", len(auths))
	}
	if auths[0] != "Bearer xai-x" || clis[0] != "" {
		t.Fatalf("first auth=%q cli=%q", auths[0], clis[0])
	}
	if auths[1] != "Bearer session-token" || clis[1] != "xai-grok-cli" {
		t.Fatalf("second auth=%q cli=%q", auths[1], clis[1])
	}
	if hosts[0] == hosts[1] {
		t.Fatalf("expected different upstream hosts, both %q", hosts[0])
	}
}

func TestDeletedFileFallsThroughToAPIKey(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	var clis []string
	hit := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		clis = append(clis, r.Header.Get("X-XAI-Token-Auth"))
		mu.Unlock()
		w.WriteHeader(200)
	}
	api := httptest.NewServer(http.HandlerFunc(hit))
	t.Cleanup(api.Close)
	cli := httptest.NewServer(http.HandlerFunc(hit))
	t.Cleanup(cli.Close)

	auth := writeAuth(t, t.TempDir(), `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "session-token",
	    "expires_at": "2099-01-01T00:00:00Z"
	  }
	}`)
	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: cli.URL,
		APIUpstream:   api.URL,
		APIKey:        "xai-x",
		AuthPath:      auth,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if err := os.Remove(auth); err != nil {
		t.Fatal(err)
	}
	res, err = http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 {
		t.Fatalf("hits=%d", len(auths))
	}
	if auths[0] != "Bearer session-token" || clis[0] != "xai-grok-cli" {
		t.Fatalf("first auth=%q cli=%q", auths[0], clis[0])
	}
	if auths[1] != "Bearer xai-x" || clis[1] != "" {
		t.Fatalf("second auth=%q cli=%q", auths[1], clis[1])
	}
}

func TestCorruptFileDoesNotFallThroughToAPIKey(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called")
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)
	auth := writeAuth(t, t.TempDir(), `{not json`)
	mux := proxy.NewMux(proxy.Config{
		APIUpstream: up.URL,
		APIKey:      "xai-x",
		AuthPath:    auth,
		HTTPClient:  up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("status=%d", res.StatusCode)
	}
}

func TestRefreshSerializedOnConcurrentRequests(t *testing.T) {
	var n atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"n","refresh_token":"nr","expires_in":3600}`))
	}))
	t.Cleanup(tok.Close)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	auth := writeAuth(t, t.TempDir(), `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "old",
	    "refresh_token": "r1",
	    "expires_at": "2020-01-01T00:00:00Z"
	  }
	}`)
	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: up.URL,
		AuthPath:      auth,
		TokenURL:      tok.URL,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := http.Get(srv.URL + "/v1/models")
			if err != nil {
				t.Error(err)
				return
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Errorf("status=%d", res.StatusCode)
			}
		}()
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("refresh calls=%d", n.Load())
	}
}

func TestProxyAPIKeyGate(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)
	mux := proxy.NewMux(proxy.Config{APIUpstream: up.URL, ProxyAPIKey: "secret", APIKey: "xai-x", HTTPClient: up.Client()})
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

func TestUpstreamV1SuffixDoesNotDouble(t *testing.T) {
	var path string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	mux := proxy.NewMux(proxy.Config{
		APIUpstream: up.URL + "/v1",
		APIKey:      "xai-x",
		AuthPath:    filepath.Join(t.TempDir(), "auth.json"),
		HTTPClient:  up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if path != "/v1/models" {
		t.Fatalf("path=%q", path)
	}
}

func TestModelsGETRetries429(t *testing.T) {
	var n atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	mux := proxy.NewMux(proxy.Config{
		APIUpstream: up.URL,
		APIKey:      "xai-x",
		AuthPath:    filepath.Join(t.TempDir(), "auth.json"),
		HTTPClient:  up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if n.Load() != 2 {
		t.Fatalf("hits=%d", n.Load())
	}
}

func TestResponsesPOSTDoesNotRetry429(t *testing.T) {
	var n atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(429)
	}))
	t.Cleanup(up.Close)

	mux := proxy.NewMux(proxy.Config{
		APIUpstream: up.URL,
		APIKey:      "xai-x",
		AuthPath:    filepath.Join(t.TempDir(), "auth.json"),
		HTTPClient:  up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"grok-4.6"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 429 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if n.Load() != 1 {
		t.Fatalf("hits=%d", n.Load())
	}
}

func TestLoopbackHost(t *testing.T) {
	if !proxy.LoopbackHost("127.0.0.1") || !proxy.LoopbackHost("localhost") || !proxy.LoopbackHost("::1") {
		t.Fatal("loopback")
	}
	if proxy.LoopbackHost("0.0.0.0") || proxy.LoopbackHost("1.2.3.4") {
		t.Fatal("non-loopback")
	}
}
