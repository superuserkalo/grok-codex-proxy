package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

func startUp(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func startProxy(t *testing.T, cfg proxy.Config) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(proxy.NewMux(cfg))
	t.Cleanup(s.Close)
	return s
}

func wantStatus(t *testing.T, url string, want int) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != want {
		t.Fatalf("GET %s status=%d want %d", url, res.StatusCode, want)
	}
}
