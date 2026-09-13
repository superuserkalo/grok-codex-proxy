package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	maxBody              = 32 << 20
	DefaultClientVersion = "1.0.30"
	DefaultOAuthUpstream = "https://cli-chat-proxy.grok.com/v1"
	DefaultAPIUpstream   = "https://api.x.ai/v1"
)

type Config struct {
	OAuthUpstream string
	APIUpstream   string
	ProxyAPIKey   string
	AuthPath      string
	OAuthToken    string
	APIKey        string
	ClientVersion string
	TokenURL      string
	HTTPClient    *http.Client
}

func (cfg Config) prepared() Config {
	if cfg.OAuthUpstream == "" {
		cfg.OAuthUpstream = DefaultOAuthUpstream
	}
	if cfg.APIUpstream == "" {
		cfg.APIUpstream = DefaultAPIUpstream
	}
	cfg.OAuthUpstream = origin(cfg.OAuthUpstream)
	cfg.APIUpstream = origin(cfg.APIUpstream)
	if cfg.ClientVersion == "" {
		cfg.ClientVersion = DefaultClientVersion
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = TokenURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return cfg
}

func origin(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") {
		return strings.TrimRight(strings.TrimSuffix(base, "/v1"), "/")
	}
	return base
}

func LoopbackHost(host string) bool {
	h := strings.Trim(host, "[]")
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

const cliTokenRejected = "token rejected by the CLI proxy; run grok login or use XAI_API_KEY\n"

type server struct {
	cfg        Config
	mu         sync.Mutex
	cond       *sync.Cond
	refreshing bool
	sess       *Store
	sessMod    time.Time
}

type creds struct {
	token    string
	upstream string
	cli      bool
	store    *Store
	status   int
	err      error
}

func NewMux(cfg Config) http.Handler {
	s := &server{cfg: cfg.prepared()}
	s.cond = sync.NewCond(&s.mu)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/models", s.serveV1)
	mux.HandleFunc("/v1/responses", s.serveV1)
	mux.HandleFunc("/v1/chat/completions", s.serveV1)
	return mux
}

func retryable(req *http.Request) bool {
	return req.Method == http.MethodGet && req.URL.Path == "/v1/models"
}

func cliRejected(resp *http.Response) bool {
	return resp.StatusCode == 401 || resp.StatusCode == 403
}

func credsFromPick(p Pick) creds {
	c := creds{token: p.Token, upstream: p.Upstream, cli: p.CLI, store: p.Store, err: p.Err}
	if p.Err != nil || p.Token == "" {
		c.status = http.StatusUnauthorized
		if c.err == nil {
			c.err = fmt.Errorf("run grok login")
		}
	}
	return c
}

func credsFromStore(st *Store, upstream string) creds {
	tok := st.AccessToken()
	c := creds{token: tok, upstream: upstream, cli: true, store: st}
	if tok == "" {
		c.status = http.StatusUnauthorized
		c.err = fmt.Errorf("run grok login")
	}
	return c
}

func (c *creds) applyRefresh(now time.Time, err error) {
	tok := ""
	if c.store != nil {
		tok = c.store.AccessToken()
	}
	switch {
	case errors.Is(err, ErrPersist) && tok != "":
		c.token, c.err, c.status = tok, nil, 0
	case err != nil && tok != "" && c.store != nil && !c.store.HardExpired(now):
		c.token, c.err, c.status = tok, err, http.StatusBadGateway
	case err != nil:
		c.token, c.err, c.status = "", err, http.StatusUnauthorized
	case tok != "":
		c.token, c.err, c.status = tok, nil, 0
	default:
		c.token, c.err, c.status = "", fmt.Errorf("run grok login"), http.StatusUnauthorized
	}
}

func (s *server) load() creds {
	if s.cfg.AuthPath == "" {
		s.sess = nil
		return credsFromPick(Resolve(s.cfg))
	}
	fi, err := os.Stat(s.cfg.AuthPath)
	if err != nil {
		s.sess = nil
		return credsFromPick(Resolve(s.cfg))
	}
	if s.sess != nil && !fi.ModTime().After(s.sessMod) {
		return credsFromStore(s.sess, s.cfg.OAuthUpstream)
	}
	st, err := LoadStore(s.cfg.AuthPath)
	if err != nil {
		s.sess = nil
		return creds{cli: true, upstream: s.cfg.OAuthUpstream, status: http.StatusUnauthorized, err: err}
	}
	s.sess = st
	s.sessMod = fi.ModTime()
	return credsFromStore(st, s.cfg.OAuthUpstream)
}

func (s *server) commitStore(store *Store, err error) {
	s.sess = store
	if err == nil {
		if fi, e := os.Stat(store.Path); e == nil {
			s.sessMod = fi.ModTime()
		}
	}
}

func (s *server) refresh(now time.Time, force bool) creds {
	s.mu.Lock()
	for s.refreshing {
		s.cond.Wait()
	}
	c := s.load()
	if c.store == nil || (!force && !c.store.NeedsRefresh(now)) {
		s.mu.Unlock()
		return c
	}
	s.refreshing = true
	store := c.store
	s.mu.Unlock()

	err := Refresh(context.Background(), store, s.cfg.HTTPClient, s.cfg.TokenURL, now)

	s.mu.Lock()
	s.refreshing = false
	s.cond.Broadcast()
	s.commitStore(store, err)
	c.applyRefresh(now, err)
	s.mu.Unlock()
	return c
}

func (s *server) serveV1(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status := 0
	model := ""
	defer func() {
		log.Printf("%s %s %d %s model=%s", r.Method, r.URL.Path, status, time.Since(start).Truncate(time.Millisecond), model)
	}()
	fail := func(code int, msg string) {
		status = code
		http.Error(w, msg, code)
	}
	if s.cfg.ProxyAPIKey != "" && r.Header.Get("Authorization") != "Bearer "+s.cfg.ProxyAPIKey {
		fail(http.StatusUnauthorized, "unauthorized\n")
		return
	}
	c := s.refresh(time.Now(), false)
	if c.status != 0 {
		msg := "run grok login\n"
		if c.err != nil {
			msg = c.err.Error() + "\n"
		}
		fail(c.status, msg)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			fail(http.StatusRequestEntityTooLarge, "request too large\n")
			return
		}
		fail(http.StatusBadRequest, "bad request\n")
		return
	}
	body, model = RewriteModel(body)
	req, err := s.upstreamRequest(r, c, body, model)
	if err != nil {
		fail(http.StatusBadRequest, "bad request\n")
		return
	}

	resp, err := s.doUpstream(req)
	if err != nil {
		fail(http.StatusBadGateway, "upstream error\n")
		return
	}
	if c.cli && cliRejected(resp) {
		code := resp.StatusCode
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		retried := false
		if c.store != nil {
			c2 := s.refresh(time.Now(), true)
			if retryable(req) && c2.status == 0 && c2.token != "" {
				req2 := req.Clone(req.Context())
				req2.Header.Set("Authorization", "Bearer "+c2.token)
				resp, err = s.doUpstream(req2)
				if err != nil {
					fail(http.StatusBadGateway, "upstream error\n")
					return
				}
				retried = true
			}
		}
		if !retried || cliRejected(resp) {
			if retried {
				code = resp.StatusCode
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			fail(code, cliTokenRejected)
			return
		}
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	status = resp.StatusCode
	w.WriteHeader(resp.StatusCode)
	CopyStream(w, resp.Body)
}

func (s *server) upstreamRequest(r *http.Request, c creds, body []byte, model string) (*http.Request, error) {
	upURL := JoinURL(c.upstream, r.URL.Path)
	if r.URL.RawQuery != "" {
		upURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	} else if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.cli {
		req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		if model != "" {
			req.Header.Set("x-grok-model-override", model)
		}
		req.Header.Set("x-grok-client-version", s.cfg.ClientVersion)
	}
	if ae := r.Header.Get("Accept"); ae != "" {
		req.Header.Set("Accept", ae)
	}
	return req, nil
}

func (s *server) doUpstream(req *http.Request) (*http.Response, error) {
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if !retryable(req) || (resp.StatusCode != 429 && resp.StatusCode < 500) {
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(200 * time.Millisecond)
	req2 := req.Clone(req.Context())
	return s.cfg.HTTPClient.Do(req2)
}
