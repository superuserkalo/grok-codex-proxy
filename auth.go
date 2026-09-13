package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	OfficialIssuer   = "https://auth.x.ai"
	OfficialClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	RefreshSkew      = 300 * time.Second
	TokenURL         = "https://auth.x.ai/oauth2/token"
)

type Store struct {
	Path    string
	entries map[string]map[string]any
	chosen  string
}

func officialKey() string {
	return OfficialIssuer + "::" + OfficialClientID
}

func LoadStore(path string) (*Store, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries map[string]map[string]any
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("auth.json: %w", err)
	}
	s := &Store{Path: path, entries: entries}
	if err := s.pick(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) pick() error {
	if _, ok := s.entries[officialKey()]; ok {
		s.chosen = officialKey()
		return nil
	}
	for k, e := range s.entries {
		if strings.HasPrefix(k, OfficialIssuer) {
			s.chosen = k
			return nil
		}
		if iss, _ := e["oidc_issuer"].(string); strings.Contains(iss, "auth.x.ai") {
			s.chosen = k
			return nil
		}
	}
	return fmt.Errorf("no auth.x.ai session in %s; run grok login", s.Path)
}

func (s *Store) Entry() string { return s.chosen }

func (s *Store) entry() map[string]any {
	if s == nil || s.chosen == "" {
		return nil
	}
	return s.entries[s.chosen]
}

func (s *Store) Get(field string) string {
	e := s.entry()
	if e == nil {
		return ""
	}
	v, _ := e[field].(string)
	return v
}

func (s *Store) AccessToken() string {
	if t := s.Get("key"); t != "" {
		return t
	}
	return s.Get("access_token")
}

func (s *Store) RefreshToken() string { return s.Get("refresh_token") }

func (s *Store) ExpiresAt() (time.Time, bool) {
	raw := s.Get("expires_at")
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
	}
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func NeedsRefresh(expiresAt, now time.Time, skew time.Duration) bool {
	if expiresAt.IsZero() {
		return true
	}
	return !now.Before(expiresAt.Add(-skew))
}

func (s *Store) NeedsRefresh(now time.Time) bool {
	exp, ok := s.ExpiresAt()
	if !ok {
		return true
	}
	return NeedsRefresh(exp, now, RefreshSkew)
}

func (s *Store) ApplyTokens(access, refresh string, expiresAt time.Time) {
	e := s.entry()
	if e == nil {
		return
	}
	e["key"] = access
	if refresh != "" {
		e["refresh_token"] = refresh
	}
	e["expires_at"] = expiresAt.UTC().Format(time.RFC3339Nano)
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) Save() error {
	b, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeAtomic(s.Path, b)
}

func RefreshIfDue(s *Store, client *http.Client, tokenURL string, now time.Time) error {
	if s == nil || !s.NeedsRefresh(now) {
		return nil
	}
	rt := s.RefreshToken()
	if rt == "" {
		return fmt.Errorf("no refresh_token; run grok login")
	}
	if client == nil {
		client = http.DefaultClient
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {OfficialClientID},
	}
	resp, err := client.PostForm(tokenURL, form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("token refresh HTTP %d; run grok login", resp.StatusCode)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &tok); err != nil {
		return fmt.Errorf("token refresh: bad json")
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("token refresh: empty access_token; run grok login")
	}
	exp := now.Add(30 * 24 * time.Hour)
	if tok.ExpiresIn > 0 {
		exp = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	s.ApplyTokens(tok.AccessToken, tok.RefreshToken, exp)
	return s.Save()
}
