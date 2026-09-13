package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/superuserkalo/grok-codex-proxy/proxy"
)

func TestLoadStorePrefersOfficialEntryAndKeyField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	body := `{
	  "https://other.example::abc": {
	    "key": "other-token",
	    "oidc_issuer": "https://other.example"
	  },
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "session-token",
	    "refresh_token": "refresh-me",
	    "expires_at": "2026-09-13T16:16:24.563568Z",
	    "oidc_issuer": "https://auth.x.ai",
	    "oidc_client_id": "b1a00492-073a-47ea-816f-4c329264a828",
	    "email": "keep-me@example.com",
	    "auth_mode": "oidc"
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := proxy.LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken() != "session-token" {
		t.Fatalf("token=%q", s.AccessToken())
	}
	if s.RefreshToken() != "refresh-me" {
		t.Fatalf("refresh=%q", s.RefreshToken())
	}
}

func TestLoadStoreAcceptsAccessTokenField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	body := `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "access_token": "alt-token",
	    "oidc_issuer": "https://auth.x.ai"
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := proxy.LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken() != "alt-token" {
		t.Fatalf("token=%q", s.AccessToken())
	}
}

func TestNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	body := `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "t",
	    "expires_at": "2099-01-01T00:00:00Z"
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := proxy.LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.ApplyTokens("t", "", now.Add(299*time.Second))
	if !s.NeedsRefresh(now) {
		t.Fatal("expected due at 299s")
	}
	s.ApplyTokens("t", "", now.Add(301*time.Second))
	if s.NeedsRefresh(now) {
		t.Fatal("expected not due at 301s")
	}
}

func TestSavePreservesUnknownFieldsAndIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	body := `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "old",
	    "refresh_token": "r1",
	    "expires_at": "2026-09-13T16:16:24Z",
	    "email": "keep-me@example.com",
	    "auth_mode": "oidc"
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := proxy.LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.ApplyTokens("new-access", "r2", time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := proxy.LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.AccessToken() != "new-access" || s2.RefreshToken() != "r2" {
		t.Fatal("tokens not updated")
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "keep-me@example.com") || !strings.Contains(string(saved), `"auth_mode": "oidc"`) {
		t.Fatalf("unknown fields dropped: %s", saved)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%o", st.Mode().Perm())
	}
}

func TestRefreshIfDuePostsFormAndSaves(t *testing.T) {
	var gotBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"n","refresh_token":"nr","expires_in":3600}`))
	}))
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	body := `{
	  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
	    "key": "old",
	    "refresh_token": "r1",
	    "expires_at": "2026-09-13T12:02:00Z"
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := proxy.LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.RefreshIfDue(s, ts.Client(), ts.URL, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, "grant_type=refresh_token") || !strings.Contains(gotBody, "refresh_token=r1") {
		t.Fatalf("form=%q", gotBody)
	}
	s2, err := proxy.LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.AccessToken() != "n" {
		t.Fatalf("token=%q", s2.AccessToken())
	}
}
