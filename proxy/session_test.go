package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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

func TestApplyRefreshInWindowDoesNotSendToken(t *testing.T) {
	path := writeSessionAuth(t, t.TempDir(), map[string]any{
		"key":        "live",
		"expires_at": time.Now().UTC().Add(100 * time.Second).Format(time.RFC3339Nano),
	})
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := newPick(SourceFile, st.AccessToken(), "https://cli.example", st, nil)
	applyRefresh(&p, ErrRefresh)
	if p.Token != "" {
		t.Fatalf("sent token=%q", p.Token)
	}
	if p.Err == nil {
		t.Fatal("expected err")
	}
	if st.AccessToken() != "live" {
		t.Fatalf("store=%q", st.AccessToken())
	}
	if code, _ := p.gate(); code != http.StatusBadGateway {
		t.Fatalf("gate=%d", code)
	}
}

func TestApplyRefreshHardExpiredDoesNotSendToken(t *testing.T) {
	path := writeSessionAuth(t, t.TempDir(), map[string]any{
		"key":        "old",
		"expires_at": "2020-01-01T00:00:00Z",
	})
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := newPick(SourceFile, st.AccessToken(), "https://cli.example", st, nil)
	applyRefresh(&p, ErrRefresh)
	if p.Token != "" {
		t.Fatalf("sent token=%q", p.Token)
	}
	if code, _ := p.gate(); code != http.StatusUnauthorized {
		t.Fatalf("gate=%d", code)
	}
}

func TestRotateForcesRefreshWhenExpiryIsFresh(t *testing.T) {
	var n atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"n","refresh_token":"nr","expires_in":3600}`))
	}))
	t.Cleanup(tok.Close)

	path := writeSessionAuth(t, t.TempDir(), map[string]any{
		"key":           "old",
		"refresh_token": "r1",
		"expires_at":    "2099-01-01T00:00:00Z",
	})
	s := newSession(Config{AuthPath: path, TokenURL: tok.URL, HTTPClient: tok.Client()})
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	p := s.live(now)
	if p.Token != "old" || n.Load() != 0 {
		t.Fatalf("live: token=%q calls=%d", p.Token, n.Load())
	}
	p = s.rotate(now)
	if p.Token != "n" || n.Load() != 1 {
		t.Fatalf("rotate: token=%q calls=%d", p.Token, n.Load())
	}
}

func TestRotateKeepsForcingAfterFailedRefresh(t *testing.T) {
	var n atomic.Int32
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(500)
	}))
	t.Cleanup(tok.Close)

	path := writeSessionAuth(t, t.TempDir(), map[string]any{
		"key":           "old",
		"refresh_token": "r1",
		"expires_at":    "2099-01-01T00:00:00Z",
	})
	s := newSession(Config{AuthPath: path, TokenURL: tok.URL, HTTPClient: tok.Client()})
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	p := s.rotate(now)
	if p.Token != "" || n.Load() != 1 {
		t.Fatalf("first rotate: token=%q calls=%d", p.Token, n.Load())
	}
	p = s.live(now)
	if p.Token != "" || n.Load() != 2 {
		t.Fatalf("live after fail: token=%q calls=%d", p.Token, n.Load())
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

func TestGateDoesNotReadStoreUnderRotation(t *testing.T) {
	path := writeSessionAuth(t, t.TempDir(), map[string]any{
		"key":        "held",
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano),
	})
	st, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p := newPick(SourceFile, "", "https://cli.example", st, nil)
	applyRefresh(&p, ErrRefresh)
	if code, _ := p.gate(); code != http.StatusBadGateway {
		t.Fatalf("gate=%d", code)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			st.ApplyTokens("rotated", "", time.Now().Add(time.Hour))
		}
	}()
	bad := 0
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if code, _ := p.gate(); code != http.StatusBadGateway {
			bad = code
			break
		}
	}
	close(stop)
	wg.Wait()
	if bad != 0 {
		t.Fatalf("gate=%d during rotation", bad)
	}
}
