package proxy_test

import (
	"context"
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

func TestServeUsesTokenWithoutExpiry(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer alt-token" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)
	auth := writeAuth(t, t.TempDir(), `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "access_token": "alt-token"
	  }
	}`)
	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: up.URL,
		AuthPath:      auth,
		HTTPClient:    up.Client(),
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

func TestRefreshSaveFailureKeepsRotatedToken(t *testing.T) {
	var n atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"n","refresh_token":"nr","expires_in":3600}`))
	}))
	t.Cleanup(tok.Close)
	var mu sync.Mutex
	var auths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	dir := t.TempDir()
	auth := writeAuth(t, dir, `{
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

	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("first status=%d", res.StatusCode)
	}
	res, err = http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("second status=%d", res.StatusCode)
	}
	if n.Load() != 1 {
		t.Fatalf("refresh calls=%d", n.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 2 || auths[0] != "Bearer n" || auths[1] != "Bearer n" {
		t.Fatalf("auths=%q", auths)
	}
}

func TestRefreshIgnoresRequestCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			close(started)
			<-release
		}
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

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		res.Body.Close()
		errCh <- nil
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("token endpoint not reached")
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request did not return")
	}
	close(release)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if n.Load() != 1 {
		t.Fatalf("refresh calls=%d", n.Load())
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

func TestRefreshFailInWindowReturns502(t *testing.T) {
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(tok.Close)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called")
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	exp := time.Now().UTC().Add(100 * time.Second).Format(time.RFC3339Nano)
	auth := writeAuth(t, t.TempDir(), `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "live",
	    "refresh_token": "r1",
	    "expires_at": "`+exp+`"
	  }
	}`)
	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: up.URL,
		AuthPath:      auth,
		TokenURL:      tok.URL,
		HTTPClient:    up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s", res.StatusCode, b)
	}
	if strings.Contains(string(b), "run grok login") && !strings.Contains(string(b), "refresh") {
		t.Fatalf("should not look like missing login: %s", b)
	}
	s, err := proxy.LoadStore(auth)
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken() != "live" {
		t.Fatalf("wiped live token: %q", s.AccessToken())
	}
}

func TestRefreshFailHardExpiredReturns401(t *testing.T) {
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(tok.Close)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called")
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
		HTTPClient:    up.Client(),
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", res.StatusCode, b)
	}
}

func TestCLI401RetriesModelsGETAfterRefresh(t *testing.T) {
	var refreshes atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"n","refresh_token":"nr","expires_in":3600}`))
	}))
	t.Cleanup(tok.Close)
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if r.Header.Get("Authorization") == "Bearer old" {
			w.WriteHeader(401)
			return
		}
		if n == 1 {
			t.Errorf("first hit should be the rejected token")
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(up.Close)

	auth := writeAuth(t, t.TempDir(), `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "old",
	    "refresh_token": "r1",
	    "expires_at": "2099-01-01T00:00:00Z"
	  }
	}`)
	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: up.URL,
		AuthPath:      auth,
		TokenURL:      tok.URL,
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
	if refreshes.Load() != 1 {
		t.Fatalf("refresh calls=%d", refreshes.Load())
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits=%d", hits.Load())
	}
}

func TestCLI401DoesNotReplayPOST(t *testing.T) {
	var refreshes atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"n","refresh_token":"nr","expires_in":3600}`))
	}))
	t.Cleanup(tok.Close)
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(401)
	}))
	t.Cleanup(up.Close)

	auth := writeAuth(t, t.TempDir(), `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "old",
	    "refresh_token": "r1",
	    "expires_at": "2099-01-01T00:00:00Z"
	  }
	}`)
	mux := proxy.NewMux(proxy.Config{
		OAuthUpstream: up.URL,
		AuthPath:      auth,
		TokenURL:      tok.URL,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	res, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"grok-4.6"}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if hits.Load() != 1 {
		t.Fatalf("upstream hits=%d", hits.Load())
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh calls=%d", refreshes.Load())
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

func TestOversizedBodyReturns413(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called")
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

	body := strings.Repeat("x", 32<<20+1)
	res, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", res.StatusCode, b)
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
