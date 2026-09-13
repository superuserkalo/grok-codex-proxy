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

type server struct {
	cfg        Config
	mu         sync.Mutex
	cond       *sync.Cond
	refreshing bool
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

func (s *server) tokenURL() string { return s.cfg.TokenURL }

func (s *server) client() *http.Client { return s.cfg.HTTPClient }

func (s *server) applyRefresh(p *Pick, now time.Time, err error) {
	if err != nil {
		tok := p.Store.AccessToken()
		if tok != "" && !p.Store.HardExpired(now) {
			p.Token = tok
			p.Err = err
			return
		}
		p.Token = ""
		p.Err = err
		return
	}
	if t := p.Store.AccessToken(); t != "" {
		p.Token = t
		p.Err = nil
		return
	}
	p.Token = ""
	p.Err = fmt.Errorf("run grok login")
}

func (s *server) bearer(now time.Time) Pick {
	s.mu.Lock()
	for s.refreshing {
		s.cond.Wait()
	}
	p := Resolve(s.cfg)
	if p.Store == nil || !p.Store.NeedsRefresh(now) {
		s.mu.Unlock()
		return p
	}
	s.refreshing = true
	store := p.Store
	s.mu.Unlock()

	err := RefreshIfDue(context.Background(), store, s.client(), s.tokenURL(), now)

	s.mu.Lock()
	s.refreshing = false
	s.cond.Broadcast()
	s.applyRefresh(&p, now, err)
	s.mu.Unlock()
	return p
}

func (s *server) serveV1(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status := 0
	model := ""
	defer func() {
		if status == 0 {
			status = 200
		}
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
	p := s.bearer(time.Now())
	if p.Err != nil {
		code := http.StatusUnauthorized
		if p.Token != "" {
			code = http.StatusBadGateway
		}
		fail(code, p.Err.Error()+"\n")
		return
	}
	if p.Token == "" {
		fail(http.StatusUnauthorized, "run grok login\n")
		return
	}
	token, upstream := p.Token, p.Upstream
	cli := p.CLI
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

	upURL := JoinURL(upstream, r.URL.Path)
	if r.URL.RawQuery != "" {
		upURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upURL, bytes.NewReader(body))
	if err != nil {
		fail(http.StatusBadRequest, "bad request\n")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	} else if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if cli {
		req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		if model != "" {
			req.Header.Set("x-grok-model-override", model)
		}
		req.Header.Set("x-grok-client-version", s.cfg.ClientVersion)
	}
	if ae := r.Header.Get("Accept"); ae != "" {
		req.Header.Set("Accept", ae)
	}

	resp, err := s.doUpstream(req)
	if err != nil {
		fail(http.StatusBadGateway, "upstream error\n")
		return
	}
	defer resp.Body.Close()

	if cli && (resp.StatusCode == 401 || resp.StatusCode == 403) {
		fail(resp.StatusCode, "token rejected by the CLI proxy; run grok login or use XAI_API_KEY\n")
		return
	}
	for _, k := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	status = resp.StatusCode
	w.WriteHeader(resp.StatusCode)
	CopyStream(w, resp.Body)
}

func (s *server) doUpstream(req *http.Request) (*http.Response, error) {
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	idempotent := req.Method == http.MethodGet && req.URL.Path == "/v1/models"
	if !idempotent || (resp.StatusCode != 429 && resp.StatusCode < 500) {
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(200 * time.Millisecond)
	req2 := req.Clone(req.Context())
	return s.client().Do(req2)
}
