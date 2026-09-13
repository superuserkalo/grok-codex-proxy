package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	OfficialIssuer   = "https://auth.x.ai"
	OfficialClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	RefreshSkew      = 300 * time.Second
	RefreshTimeout   = 15 * time.Second
	TokenURL         = "https://auth.x.ai/oauth2/token"
)

var (
	ErrRefresh = errors.New("token refresh")
	ErrPersist = errors.New("auth persist")
)

type Source string

const (
	SourceNone  Source = ""
	SourceFile  Source = "file"
	SourceOAuth Source = "oauth-env"
	SourceAPI   Source = "api-key"
)

type Pick struct {
	Token    string
	Upstream string
	Source   Source
	Store    *Store
	Err      error
}

func newPick(src Source, token, upstream string, st *Store, err error) Pick {
	return Pick{Token: token, Upstream: upstream, Source: src, Store: st, Err: err}
}

func (p Pick) CLI() bool { return p.Source != SourceAPI }

func Resolve(cfg Config) Pick {
	cfg = cfg.prepared()
	oauth, api := cfg.OAuthUpstream, cfg.APIUpstream
	var missing error
	if cfg.AuthPath != "" {
		st, err := LoadStore(cfg.AuthPath)
		if err == nil {
			return newPick(SourceFile, st.AccessToken(), oauth, st, nil)
		}
		if !os.IsNotExist(err) {
			return newPick(SourceFile, "", oauth, nil, err)
		}
		missing = err
	}
	if cfg.OAuthToken != "" {
		return newPick(SourceOAuth, cfg.OAuthToken, oauth, nil, nil)
	}
	if cfg.APIKey != "" {
		return newPick(SourceAPI, cfg.APIKey, api, nil, nil)
	}
	err := fmt.Errorf("run grok login")
	if missing != nil {
		err = missing
	}
	return newPick(SourceNone, "", oauth, nil, err)
}

func (p Pick) gate() (int, string) {
	if p.Err == nil && p.Token != "" {
		return 0, ""
	}
	msg := "run grok login\n"
	if p.Err != nil {
		msg = p.Err.Error() + "\n"
	}
	if p.Err != nil && p.Store != nil && p.Store.AccessToken() != "" && !p.Store.HardExpired(time.Now()) {
		return http.StatusBadGateway, msg
	}
	return http.StatusUnauthorized, msg
}

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
	if err := s.choose(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) choose() error {
	if _, ok := s.entries[officialKey()]; ok {
		s.chosen = officialKey()
		return nil
	}
	keys := make([]string, 0, len(s.entries))
	for k := range s.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		iss, _, ok := strings.Cut(k, "::")
		if ok && iss == OfficialIssuer {
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

func (s *Store) get(field string) string {
	e := s.entry()
	if e == nil {
		return ""
	}
	v, _ := e[field].(string)
	return v
}

func (s *Store) AccessToken() string {
	if t := s.get("key"); t != "" {
		return t
	}
	return s.get("access_token")
}

func (s *Store) RefreshToken() string { return s.get("refresh_token") }

func (s *Store) ExpiresAt() (time.Time, bool) {
	raw := s.get("expires_at")
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

func (s *Store) NeedsRefresh(now time.Time) bool {
	if s == nil {
		return false
	}
	exp, ok := s.ExpiresAt()
	if !ok {
		return false
	}
	return !now.Before(exp.Add(-RefreshSkew))
}

func (s *Store) HardExpired(now time.Time) bool {
	exp, ok := s.ExpiresAt()
	if !ok {
		return false
	}
	return !now.Before(exp)
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

func WriteAtomic(path string, data []byte) error {
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
	return WriteAtomic(s.Path, b)
}

func Refresh(ctx context.Context, s *Store, client *http.Client, tokenURL string, now time.Time) error {
	if s == nil {
		return fmt.Errorf("%w: no session", ErrRefresh)
	}
	rt := s.RefreshToken()
	if rt == "" {
		return fmt.Errorf("%w: no refresh_token", ErrRefresh)
	}
	if client == nil {
		client = http.DefaultClient
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {s.clientID()},
	}
	ctx, cancel := context.WithTimeout(ctx, RefreshTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: %s", ErrRefresh, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrRefresh, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %s", ErrRefresh, err)
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: HTTP %d", ErrRefresh, resp.StatusCode)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &tok); err != nil {
		return fmt.Errorf("%w: bad json", ErrRefresh)
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("%w: empty access_token", ErrRefresh)
	}
	if tok.ExpiresIn <= 0 {
		return fmt.Errorf("%w: missing expires_in", ErrRefresh)
	}
	exp := now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	s.ApplyTokens(tok.AccessToken, tok.RefreshToken, exp)
	if err := s.Save(); err != nil {
		return fmt.Errorf("%w: %s", ErrPersist, err)
	}
	return nil
}

func applyRefresh(p *Pick, err error) {
	tok := ""
	if p.Store != nil {
		tok = p.Store.AccessToken()
	}
	if err == nil || errors.Is(err, ErrPersist) {
		if tok != "" {
			p.Token, p.Err = tok, nil
			return
		}
		p.Token, p.Err = "", fmt.Errorf("run grok login")
		return
	}
	p.Token, p.Err = "", err
}

func (s *Store) clientID() string {
	_, id, ok := strings.Cut(s.chosen, "::")
	if ok && id != "" {
		return id
	}
	return OfficialClientID
}

type session struct {
	cfg   Config
	mu    sync.Mutex
	store *Store
	mod   time.Time
	stale bool
}

func newSession(cfg Config) *session {
	return &session{cfg: cfg.prepared()}
}

func (s *session) setStore(st *Store) {
	s.store = st
	if st == nil {
		s.mod = time.Time{}
	}
}

func (s *session) noteDisk() {
	if s.store == nil {
		s.mod = time.Time{}
		return
	}
	if fi, err := os.Stat(s.store.Path); err == nil {
		s.mod = fi.ModTime()
	}
}

func (s *session) cachedStore() *Store {
	if s.cfg.AuthPath == "" || s.store == nil {
		return nil
	}
	fi, err := os.Stat(s.cfg.AuthPath)
	if err != nil {
		return nil
	}
	if fi.ModTime().After(s.mod) {
		return nil
	}
	return s.store
}

func (s *session) load() Pick {
	if st := s.cachedStore(); st != nil {
		return newPick(SourceFile, st.AccessToken(), s.cfg.OAuthUpstream, st, nil)
	}
	p := Resolve(s.cfg)
	s.setStore(p.Store)
	s.noteDisk()
	s.stale = false
	return p
}

func (s *session) live(now time.Time) Pick {
	return s.refresh(now, false)
}

func (s *session) rotate(now time.Time) Pick {
	return s.refresh(now, true)
}

func (s *session) refresh(now time.Time, force bool) Pick {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.load()
	if p.Store == nil {
		return p
	}
	if force {
		s.stale = true
	}
	if !s.stale && !p.Store.NeedsRefresh(now) {
		return p
	}
	err := Refresh(context.Background(), p.Store, s.cfg.HTTPClient, s.cfg.TokenURL, now)
	applyRefresh(&p, err)
	s.setStore(p.Store)
	if err == nil {
		s.noteDisk()
	}
	if p.Err == nil {
		s.stale = false
	}
	return p
}
