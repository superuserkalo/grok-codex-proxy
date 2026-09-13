# grok-codex-proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a tiny Go stdlib binary that serves an OpenAI-compatible `/v1` on loopback, authenticating to xAI’s official CLI chat proxy with the Grok CLI OAuth session in `~/.grok/auth.json`.

**Architecture:** One `package main` process. `auth.go` reads/refreshes the official auth file. `forward.go` rewrites the model slug, injects CLI headers, and copies bytes (SSE included). `server.go` binds loopback, gates on optional `PROXY_API_KEY`, and exposes `/healthz` plus `/v1/*`. `codex.go` surgically upserts Codex’s `[model_providers.xai-oauth]` table. `main.go` is `status` / `serve`.

**Tech Stack:** Go 1.22+, stdlib only (`flag`, `net/http`, `encoding/json`, `net/http/httptest` in tests). MIT.

**Spec:** `docs/design.md`

## Global Constraints

- Stdlib only. No Cobra, no TOML library, no extra HTTP client.
- Default listen `127.0.0.1:8787`. Refuse non-loopback bind unless `PROXY_API_KEY` is set.
- OAuth upstream `https://cli-chat-proxy.grok.com/v1` with headers `Authorization: Bearer`, `X-XAI-Token-Auth: xai-grok-cli`, `x-grok-model-override: <model>`.
- API key upstream `https://api.x.ai/v1` with Bearer only.
- No `login` command. No images/video/TTS. No telemetry. Never log tokens or `auth.json`.
- Refresh 300s before `expires_at`. Atomic writes, mode `0600`.
- `serve` upserts `[model_providers.xai-oauth]` only; never change top-level `model` / `model_provider`.
- Module path: `github.com/superuserkalo/grok-codex-proxy`.
- Commit after every task. Never copy `~/.grok/auth.json` into the repo.

## File map

| File | Responsibility |
| --- | --- |
| `go.mod` | Module |
| `.gitignore` | Binary, `.env` |
| `LICENSE` | MIT |
| `slug.go` / `slug_test.go` | Model slug map |
| `auth.go` / `auth_test.go` | auth.json load, pick, refresh, atomic save |
| `codex.go` / `codex_test.go` | Surgical Codex provider-table upsert |
| `forward.go` / `forward_test.go` | Body model rewrite, SSE copy |
| `server.go` / `server_test.go` | Mux, gate token, healthz, bind rules |
| `main.go` / `main_test.go` | `status` / `serve` CLI |
| `README.md`, `AGENTS.md`, `CLAUDE.md`, `docs/*.md`, `.env.example` | Docs |

## Agreed test seams (from spec)

1. `MapModel(slug) string`
2. `LoadStore` / chosen entry / `AccessToken`
3. `NeedsRefresh` (300s) + `ApplyTokens` + atomic `Save` + `RefreshIfDue`
4. `UpsertProvider` (insert, replace, leave other keys)
5. SSE copy + `/v1` forward via `httptest`

## Type names (do not drift)

`MapModel`, `Store`, `LoadStore`, `NeedsRefresh`, `ApplyTokens`, `RefreshIfDue`, `UpsertProvider`, `Config`, `NewMux`, `rewriteModel`, `copyStream`, `loopbackHost`, `OfficialIssuer`, `OfficialClientID`, `RefreshSkew`, `TokenURL`, `writeAtomic`.

---

### Task 1: Module, slug map

**Files:**
- Create: `go.mod`, `.gitignore`, `LICENSE`, `slug.go`, `slug_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `func MapModel(slug string) string`

- [ ] **Step 1: Write the failing test**

Create `slug_test.go`:

```go
package main

import "testing"

func TestMapModel(t *testing.T) {
	cases := map[string]string{
		"grok-4.6":            "grok-4.6",
		"xai/grok-4.6":        "grok-4.6",
		"grok-oauth/grok-4.6": "grok-4.6",
		"grok-4.5":            "grok-4.5",
		"xai/grok-4.5":        "grok-4.5",
		"grok-oauth/grok-4.5": "grok-4.5",
		"grok-4-fast":         "grok-4-fast",
		"xai/custom-thing":    "custom-thing",
	}
	for in, want := range cases {
		if got := MapModel(in); got != want {
			t.Fatalf("MapModel(%q)=%q want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./...`
Expected: FAIL, `MapModel` undefined

- [ ] **Step 3: Write minimal implementation**

`go.mod`:

```
module github.com/superuserkalo/grok-codex-proxy

go 1.22
```

`.gitignore`:

```
/grok-codex-proxy
*.exe
.env
```

`LICENSE`:

```
MIT License

Copyright (c) 2026

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

`slug.go`:

```go
package main

import "strings"

func MapModel(slug string) string {
	for _, p := range []string{"xai/", "grok-oauth/"} {
		slug = strings.TrimPrefix(slug, p)
	}
	return slug
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add go.mod .gitignore LICENSE slug.go slug_test.go
git commit -m "feat: map Codex model slugs to upstream ids"
```

---

### Task 2: auth.json parse

**Files:**
- Create: `auth.go`, `auth_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `type Store struct`; `func LoadStore(path string) (*Store, error)`; `func (s *Store) AccessToken() string`; `func (s *Store) RefreshToken() string`; `func (s *Store) ExpiresAt() (time.Time, bool)`; `func (s *Store) Get(field string) string`; `func (s *Store) Entry() string`; `const OfficialIssuer = "https://auth.x.ai"`; `const OfficialClientID = "b1a00492-073a-47ea-816f-4c329264a828"`; `const RefreshSkew = 300 * time.Second`

Prefer entry `https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828`. Else first key prefixed `https://auth.x.ai`, else first entry whose `oidc_issuer` contains `auth.x.ai`. Token from `key`, else `access_token`. Keep the file as `map[string]map[string]any`.

- [ ] **Step 1: Write the failing test**

Create `auth_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
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
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken() != "session-token" {
		t.Fatalf("token=%q", s.AccessToken())
	}
	if s.RefreshToken() != "refresh-me" {
		t.Fatalf("refresh=%q", s.RefreshToken())
	}
	if s.Get("email") != "keep-me@example.com" {
		t.Fatalf("email not preserved: %q", s.Get("email"))
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
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.AccessToken() != "alt-token" {
		t.Fatalf("token=%q", s.AccessToken())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./...`
Expected: FAIL, `LoadStore` undefined

- [ ] **Step 3: Write minimal implementation**

Create `auth.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
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
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add auth.go auth_test.go
git commit -m "feat: parse official grok auth.json sessions"
```

---

### Task 3: Refresh schedule, apply tokens, atomic save, refresh grant

**Files:**
- Modify: `auth.go`, `auth_test.go`

**Interfaces:**
- Consumes: `Store` from Task 2
- Produces: `func NeedsRefresh(expiresAt, now time.Time, skew time.Duration) bool`; `func (s *Store) NeedsRefresh(now time.Time) bool`; `func (s *Store) ApplyTokens(access, refresh string, expiresAt time.Time)`; `func (s *Store) Save() error`; `func RefreshIfDue(s *Store, client *http.Client, tokenURL string, now time.Time) error`; `func writeAtomic(path string, data []byte) error`

`NeedsRefresh` is true if expiry is zero or `!now.Before(expiresAt.Add(-skew))` with skew `RefreshSkew` (300s).

`Save` marshals `s.entries` with indent, writes `path+".tmp"` in the same directory, `chmod 0600`, `Rename` over `path`.

`RefreshIfDue`: if not due, nil. POST form `grant_type=refresh_token`, `refresh_token`, `client_id`. Parse `access_token`, optional `refresh_token`, `expires_in`. Expiry is `now + expires_in`; if `expires_in` missing, 30 days. Do not log the response body.

- [ ] **Step 1: Write the failing tests**

Append to `auth_test.go` (add imports `io`, `net/http`, `net/http/httptest`, `strings`, `time`):

```go
func TestNeedsRefresh(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	if !NeedsRefresh(now.Add(299*time.Second), now, RefreshSkew) {
		t.Fatal("expected due at 299s")
	}
	if NeedsRefresh(now.Add(301*time.Second), now, RefreshSkew) {
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
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	s.ApplyTokens("new-access", "r2", time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC))
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s2, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.AccessToken() != "new-access" || s2.RefreshToken() != "r2" {
		t.Fatal("tokens not updated")
	}
	if s2.Get("email") != "keep-me@example.com" || s2.Get("auth_mode") != "oidc" {
		t.Fatal("unknown fields dropped")
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
	s, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RefreshIfDue(s, ts.Client(), ts.URL, now); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, "grant_type=refresh_token") || !strings.Contains(gotBody, "refresh_token=r1") {
		t.Fatalf("form=%q", gotBody)
	}
	s2, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s2.AccessToken() != "n" {
		t.Fatalf("token=%q", s2.AccessToken())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./...`
Expected: FAIL, `NeedsRefresh` undefined

- [ ] **Step 3: Write minimal implementation**

Append to `auth.go` (add imports `io`, `net/http`, `net/url`):

```go
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
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add auth.go auth_test.go
git commit -m "feat: refresh grok OAuth tokens into auth.json"
```

---

### Task 4: Codex provider-table upsert

**Files:**
- Create: `codex.go`, `codex_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `func UpsertProvider(src, baseURL, envKey string) string`

Replace existing `[model_providers.xai-oauth]` from that header line until the next line that starts with `[`, or EOF. If missing, append after a blank line. Never rewrite `model =` / `model_provider =`. `env_key` only when `envKey != ""`.

- [ ] **Step 1: Write the failing test**

Create `codex_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestUpsertProviderInsertsAndReplacesWithoutTouchingModel(t *testing.T) {
	src := "model = \"gpt-6-astra\"\nmodel_provider = \"openai\"\n\n[notice]\nfast_default_opt_out = true\n"
	got := UpsertProvider(src, "http://127.0.0.1:8787/v1", "")
	if !strings.Contains(got, "model = \"gpt-6-astra\"") {
		t.Fatalf("model line lost:\n%s", got)
	}
	if !strings.Contains(got, "model_provider = \"openai\"") {
		t.Fatalf("model_provider lost:\n%s", got)
	}
	if !strings.Contains(got, "[model_providers.xai-oauth]") || !strings.Contains(got, `base_url = "http://127.0.0.1:8787/v1"`) {
		t.Fatalf("provider missing:\n%s", got)
	}
	if strings.Contains(got, "env_key") {
		t.Fatalf("unexpected env_key:\n%s", got)
	}

	got2 := UpsertProvider(got, "http://127.0.0.1:9999/v1", "GROK_CODEX_PROXY_KEY")
	if count := strings.Count(got2, "[model_providers.xai-oauth]"); count != 1 {
		t.Fatalf("tables=%d\n%s", count, got2)
	}
	if !strings.Contains(got2, `base_url = "http://127.0.0.1:9999/v1"`) {
		t.Fatalf("base_url not replaced:\n%s", got2)
	}
	if strings.Contains(got2, "8787") {
		t.Fatalf("old port lingered:\n%s", got2)
	}
	if !strings.Contains(got2, `env_key = "GROK_CODEX_PROXY_KEY"`) {
		t.Fatalf("env_key missing:\n%s", got2)
	}
	if !strings.Contains(got2, "model = \"gpt-6-astra\"") {
		t.Fatalf("model clobbered:\n%s", got2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./...`
Expected: FAIL, `UpsertProvider` undefined

- [ ] **Step 3: Write minimal implementation**

Create `codex.go`:

```go
package main

import "strings"

const providerHeader = "[model_providers.xai-oauth]"

func providerTable(baseURL, envKey string) string {
	var b strings.Builder
	b.WriteString(providerHeader)
	b.WriteString("\nname = \"xAI Grok OAuth (local)\"\n")
	b.WriteString("base_url = \"")
	b.WriteString(baseURL)
	b.WriteString("\"\nwire_api = \"responses\"\n")
	if envKey != "" {
		b.WriteString("env_key = \"")
		b.WriteString(envKey)
		b.WriteString("\"\n")
	}
	return b.String()
}

func UpsertProvider(src, baseURL, envKey string) string {
	table := providerTable(baseURL, envKey)
	lines := splitKeepEnd(src)
	start, end, found := findTable(lines, providerHeader)
	if !found {
		out := strings.TrimRight(src, "\n")
		if out != "" {
			out += "\n\n"
		}
		return out + table
	}
	var b strings.Builder
	for i := 0; i < start; i++ {
		b.WriteString(lines[i])
	}
	b.WriteString(table)
	for i := end; i < len(lines); i++ {
		b.WriteString(lines[i])
	}
	return b.String()
}

func splitKeepEnd(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			lines = append(lines, s)
			break
		}
		lines = append(lines, s[:i+1])
		s = s[i+1:]
		if s == "" {
			break
		}
	}
	return lines
}

func findTable(lines []string, header string) (start, end int, found bool) {
	for i, ln := range lines {
		trim := strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if trim != header {
			continue
		}
		start = i
		end = len(lines)
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(strings.TrimSuffix(lines[j], "\r"))
			if strings.HasPrefix(t, "[") {
				end = j
				break
			}
		}
		return start, end, true
	}
	return 0, 0, false
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add codex.go codex_test.go
git commit -m "feat: upsert Codex xai-oauth provider table"
```

---

### Task 5: Forward, SSE copy, HTTP mux

**Files:**
- Create: `forward.go`, `forward_test.go`, `server.go`, `server_test.go`

**Interfaces:**
- Consumes: `MapModel`, `LoadStore`, `RefreshIfDue`
- Produces: `type Config struct`; `func NewMux(cfg Config) http.Handler`; `func rewriteModel(body []byte) (out []byte, model string)`; `func copyStream(dst http.ResponseWriter, src io.Reader)`; `func (cfg Config) listenAddr() string`; `func loopbackHost(host string) bool`

`Config` fields: `Host string`, `Port int`, `Upstream string`, `UseCLIHeaders bool`, `ProxyAPIKey string`, `AuthPath string`, `TokenURL string`, `OAuthToken string`, `APIKey string`, `HTTPClient *http.Client`, `Now func() time.Time`.

- [ ] **Step 1: Write the failing tests**

Create `forward_test.go`:

```go
package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRewriteModel(t *testing.T) {
	out, model := rewriteModel([]byte(`{"model":"xai/grok-4.6","stream":true,"input":"hi"}`))
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

func TestCopyStreamWritesSSEChunks(t *testing.T) {
	src := strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"p\"}\n\ndata: [DONE]\n\n")
	rr := httptest.NewRecorder()
	copyStream(rr, src)
	got := rr.Body.String()
	if !strings.Contains(got, "response.output_text.delta") || !strings.Contains(got, "[DONE]") {
		t.Fatalf("sse=%q", got)
	}
}
```

Create `server_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./...`
Expected: FAIL, `rewriteModel` / `NewMux` undefined

- [ ] **Step 3: Write minimal implementation**

Create `forward.go`:

```go
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func rewriteModel(body []byte) ([]byte, string) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, ""
	}
	raw, _ := m["model"].(string)
	if raw == "" {
		return body, ""
	}
	mapped := MapModel(raw)
	m["model"] = mapped
	out, err := json.Marshal(m)
	if err != nil {
		return body, mapped
	}
	return out, mapped
}

func copyStream(dst http.ResponseWriter, src io.Reader) {
	buf := make([]byte, 32*1024)
	fl, _ := dst.(http.Flusher)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			_, _ = dst.Write(buf[:n])
			if fl != nil {
				fl.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}
```

Create `server.go`:

```go
package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const maxBody = 32 << 20

type Config struct {
	Host          string
	Port          int
	Upstream      string
	UseCLIHeaders bool
	ProxyAPIKey   string
	AuthPath      string
	TokenURL      string
	OAuthToken    string
	APIKey        string
	HTTPClient    *http.Client
	Now           func() time.Time
}

func (cfg Config) client() *http.Client {
	if cfg.HTTPClient != nil {
		return cfg.HTTPClient
	}
	return http.DefaultClient
}

func (cfg Config) now() time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now()
}

func (cfg Config) listenAddr() string {
	return net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
}

func loopbackHost(host string) bool {
	h := strings.Trim(host, "[]")
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func NewMux(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	h := func(w http.ResponseWriter, r *http.Request) {
		cfg.serveV1(w, r)
	}
	mux.HandleFunc("/v1/models", h)
	mux.HandleFunc("/v1/responses", h)
	mux.HandleFunc("/v1/chat/completions", h)
	return mux
}

func (cfg Config) serveV1(w http.ResponseWriter, r *http.Request) {
	if cfg.ProxyAPIKey != "" && r.Header.Get("Authorization") != "Bearer "+cfg.ProxyAPIKey {
		http.Error(w, "unauthorized\n", http.StatusUnauthorized)
		return
	}
	token, err := cfg.bearer()
	if err != nil || token == "" {
		http.Error(w, "run grok login\n", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	body, model := rewriteModel(body)

	upURL := joinURL(cfg.Upstream, r.URL.Path)
	if r.URL.RawQuery != "" {
		upURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "bad request\n", http.StatusBadRequest)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	} else if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if cfg.UseCLIHeaders {
		req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		if model != "" {
			req.Header.Set("x-grok-model-override", model)
		}
	}
	if ae := r.Header.Get("Accept"); ae != "" {
		req.Header.Set("Accept", ae)
	}

	resp, err := cfg.doUpstream(req)
	if err != nil {
		http.Error(w, "upstream error\n", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if cfg.UseCLIHeaders && (resp.StatusCode == 401 || resp.StatusCode == 403) {
		http.Error(w, "token rejected by the CLI proxy; run grok login or use XAI_API_KEY\n", resp.StatusCode)
		return
	}
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	copyStream(w, resp.Body)
}

func (cfg Config) doUpstream(req *http.Request) (*http.Response, error) {
	resp, err := cfg.client().Do(req)
	if err != nil {
		return nil, err
	}
	idempotent := req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/models")
	if !idempotent || (resp.StatusCode != 429 && resp.StatusCode < 500) {
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(200 * time.Millisecond)
	req2 := req.Clone(req.Context())
	return cfg.client().Do(req2)
}

func (cfg Config) bearer() (string, error) {
	if cfg.AuthPath != "" {
		if _, err := os.Stat(cfg.AuthPath); err == nil {
			s, err := LoadStore(cfg.AuthPath)
			if err != nil {
				return "", err
			}
			tokenURL := cfg.TokenURL
			if tokenURL == "" {
				tokenURL = TokenURL
			}
			if err := RefreshIfDue(s, cfg.client(), tokenURL, cfg.now()); err != nil {
				return "", err
			}
			if t := s.AccessToken(); t != "" {
				return t, nil
			}
			return "", fmt.Errorf("run grok login")
		}
	}
	if cfg.OAuthToken != "" {
		return cfg.OAuthToken, nil
	}
	if cfg.APIKey != "" {
		return cfg.APIKey, nil
	}
	return "", fmt.Errorf("run grok login")
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add forward.go forward_test.go server.go server_test.go
git commit -m "feat: forward /v1 to the Grok CLI chat proxy"
```

---

### Task 6: CLI `status` / `serve`

**Files:**
- Create: `main.go`, `main_test.go`

**Interfaces:**
- Consumes: `LoadStore`, `RefreshIfDue`, `NewMux`, `UpsertProvider`, `loopbackHost`, `writeAtomic`
- Produces: binary `grok-codex-proxy`

- [ ] **Step 1: Write a CLI smoke test**

Create `main_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUsageExit(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "p")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %s", out)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "GROK_HOME="+t.TempDir())
	if err := cmd.Run(); err == nil {
		t.Fatal("expected nonzero")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./...`
Expected: FAIL (no `main`)

- [ ] **Step 3: Write `main.go`**

```go
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const usage = `grok-codex-proxy status
grok-codex-proxy serve [--host 127.0.0.1] [--port 8787] [--no-write-config]
`

func main() {
	os.Exit(run(os.Args))
}

func run(args []string) int {
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[1] {
	case "status":
		return cmdStatus()
	case "serve":
		return cmdServe(args[2:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stderr, usage)
		return 2
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", args[1], usage)
		return 2
	}
}

func grokHome() string {
	if v := os.Getenv("GROK_HOME"); v != "" {
		return v
	}
	return filepath.Join(os.Getenv("HOME"), ".grok")
}

func authPath() string { return filepath.Join(grokHome(), "auth.json") }

func cmdStatus() int {
	path := authPath()
	fmt.Printf("auth_path=%s\n", path)
	s, err := LoadStore(path)
	if err != nil {
		fmt.Printf("session=none\nerror=%s\n", err)
		if os.Getenv("GROK_OAUTH_TOKEN") == "" && os.Getenv("XAI_API_KEY") == "" {
			return 1
		}
		fmt.Println("fallback=env")
		return 0
	}
	fmt.Printf("entry=%s\n", s.Entry())
	if exp, ok := s.ExpiresAt(); ok {
		fmt.Printf("expires_at=%s\n", exp.UTC().Format(time.RFC3339Nano))
		fmt.Printf("needs_refresh=%t\n", s.NeedsRefresh(time.Now()))
	} else {
		fmt.Println("expires_at=unknown")
		fmt.Println("needs_refresh=true")
	}
	fmt.Printf("upstream=%s\n", oauthUpstream())
	if s.AccessToken() == "" {
		return 1
	}
	return 0
}

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "listen host")
	port := fs.Int("port", 8787, "listen port")
	noWrite := fs.Bool("no-write-config", false, "do not upsert ~/.codex/config.toml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !loopbackHost(*host) && os.Getenv("PROXY_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "refusing non-loopback bind without PROXY_API_KEY")
		return 2
	}
	cfg := Config{
		Host:        *host,
		Port:        *port,
		AuthPath:    authPath(),
		TokenURL:    TokenURL,
		OAuthToken:  os.Getenv("GROK_OAUTH_TOKEN"),
		APIKey:      os.Getenv("XAI_API_KEY"),
		ProxyAPIKey: os.Getenv("PROXY_API_KEY"),
	}
	cfg.UseCLIHeaders, cfg.Upstream = resolveUpstream(cfg)
	if !*noWrite {
		if err := writeCodexConfig(*host, *port, cfg.ProxyAPIKey != ""); err != nil {
			fmt.Fprintf(os.Stderr, "codex config: %v\n", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "wrote Codex provider table")
	}
	addr := cfg.listenAddr()
	fmt.Fprintf(os.Stderr, "listening on http://%s\n", addr)
	if err := http.ListenAndServe(addr, NewMux(cfg)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func oauthUpstream() string {
	if v := os.Getenv("XAI_BASE_URL"); v != "" {
		return v
	}
	if v := os.Getenv("GROK_CLI_CHAT_PROXY_BASE_URL"); v != "" {
		return v
	}
	return "https://cli-chat-proxy.grok.com/v1"
}

func apiUpstream() string {
	if v := os.Getenv("XAI_BASE_URL"); v != "" {
		return v
	}
	return "https://api.x.ai/v1"
}

func resolveUpstream(cfg Config) (bool, string) {
	if _, err := os.Stat(cfg.AuthPath); err == nil {
		return true, oauthUpstream()
	}
	if cfg.OAuthToken != "" {
		return true, oauthUpstream()
	}
	if cfg.APIKey != "" {
		return false, apiUpstream()
	}
	return true, oauthUpstream()
}

func writeCodexConfig(host string, port int, gate bool) error {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".codex")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	path := filepath.Join(home, "config.toml")
	src := ""
	if b, err := os.ReadFile(path); err == nil {
		src = string(b)
	} else if !os.IsNotExist(err) {
		return err
	}
	base := fmt.Sprintf("http://%s:%d/v1", host, port)
	envKey := ""
	if gate {
		envKey = "GROK_CODEX_PROXY_KEY"
	}
	return writeAtomic(path, []byte(UpsertProvider(src, base, envKey)))
}
```

Never print `key`, `access_token`, or `refresh_token` values.

- [ ] **Step 4: Run the tests and make sure they pass**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add status and serve CLI"
```

---

### Task 7: Docs and env example

**Files:**
- Create: `README.md`, `AGENTS.md`, `CLAUDE.md`, `docs/codex.md`, `docs/auth.md`, `docs/troubleshooting.md`, `.env.example`
- Leave `docs/design.md` and `docs/plan.md` as-is

README: what this is / is not; install (`go install github.com/superuserkalo/grok-codex-proxy@latest` and `go build`); `grok login`; `grok-codex-proxy status`; `grok-codex-proxy serve`; Codex invoke `codex -c model_provider="xai-oauth" -m grok-4.6 "..."`; subscription billing vs `XAI_API_KEY`; security (loopback, no token logs, `0600`); verify steps from the spec.

`AGENTS.md`: layout, `go test ./...`, privacy, YAGNI, stdlib only.

`CLAUDE.md`: one line pointing at `AGENTS.md`.

`.env.example`: `GROK_HOME=`, `PROXY_API_KEY=`, `GROK_CODEX_PROXY_KEY=`, `XAI_BASE_URL=`, `GROK_CLI_CHAT_PROXY_BASE_URL=` with comments, no secret values.

- [ ] **Step 1: Write the docs files** to match `docs/design.md`. No new behavior.

- [ ] **Step 2: Commit**

```bash
git add README.md AGENTS.md CLAUDE.md docs/codex.md docs/auth.md docs/troubleshooting.md .env.example
git commit -m "docs: README, agent pointers, Codex and auth notes"
```

---

### Task 8: Public GitHub repo

**Files:** none (remote)

- [ ] **Step 1:** `gh repo create grok-codex-proxy --public --source=. --remote=origin --description "Local OpenAI-compatible proxy so Codex can use a Grok CLI OAuth session"`
- [ ] **Step 2:** `git push -u origin main`
- [ ] **Step 3:** Confirm the GitHub URL. Do not push `auth.json` or `.env`. After push, run a ponytail-review on the tree and delete anything that is not in the spec.

---

## Spec coverage

| Spec item | Task |
| --- | --- |
| Slug map | 1 |
| auth.json parse, official entry, `key`/`access_token` | 2 |
| Refresh 300s, atomic 0600, preserve fields | 3 |
| Codex provider upsert, no default model steal | 4 |
| `/healthz`, `/v1/*`, SSE, CLI headers, retries, gate | 5 |
| `status` / `serve`, bind rules, env upstream | 6 |
| README, AGENTS, CLAUDE, docs/*, `.env.example` | 7 |
| Public `gh` repo | 8 |
| No login, no images, no telemetry, no token logs | constraints |
