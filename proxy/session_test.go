package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func writeSessionAuth(t *testing.T, dir string, fields map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, "auth.json")
	key := OfficialIssuer + "::" + OfficialClientID
	b, err := json.MarshalIndent(map[string]map[string]any{key: fields}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionKeepsStoreIfSaveFails(t *testing.T) {
	var n atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"n","refresh_token":"nr","expires_in":3600}`))
	}))
	t.Cleanup(tok.Close)

	dir := t.TempDir()
	path := writeSessionAuth(t, dir, map[string]any{
		"key":           "old",
		"refresh_token": "r1",
		"expires_at":    "2020-01-01T00:00:00Z",
	})
	s := newSession(Config{AuthPath: path, TokenURL: tok.URL, HTTPClient: tok.Client()})
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	p := s.live(now)
	if p.Err != nil || p.Token != "n" || p.Store == nil {
		t.Fatalf("first live: token=%q err=%v", p.Token, p.Err)
	}
	p = s.live(now)
	if p.Token != "n" || n.Load() != 1 {
		t.Fatalf("cached after persist fail: token=%q calls=%d", p.Token, n.Load())
	}
}

func TestSessionReloadsWhenAuthFileIsNewer(t *testing.T) {
	dir := t.TempDir()
	path := writeSessionAuth(t, dir, map[string]any{
		"key":        "old",
		"expires_at": "2099-01-01T00:00:00Z",
	})
	s := newSession(Config{AuthPath: path})
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	p := s.live(now)
	if p.Token != "old" {
		t.Fatalf("first token=%q", p.Token)
	}

	writeSessionAuth(t, dir, map[string]any{
		"key":        "new",
		"expires_at": "2099-01-01T00:00:00Z",
	})
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	p = s.live(now)
	if p.Token != "new" {
		t.Fatalf("reloaded token=%q", p.Token)
	}
}
