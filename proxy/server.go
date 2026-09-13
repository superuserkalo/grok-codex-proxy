package proxy

import (
	"bytes"
	"context"
	"errors"
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

func pickStatus(p Pick) int {
	if p.Err == nil && p.Token != "" {
		return 0
	}
	if p.Err != nil && p.Token != "" {
		return http.StatusBadGateway
	}
	return http.StatusUnauthorized
}

func (s *server) cached() *Store {
	if s.cfg.AuthPath == "" || s.sess == nil {
		return nil
	}
	fi, err := os.Stat(s.cfg.AuthPath)
	if err != nil {
		return nil
	}
	if !fi.ModTime().After(s.sessMod) {
		return s.sess
	}
	return nil
}

func (s *server) remember(p Pick) {
	if p.Store == nil {
		s.sess = nil
		return
	}
	s.sess = p.Store
	if fi, err := os.Stat(p.Store.Path); err == nil {
		s.sessMod = fi.ModTime()
	}
}

func (s *server) load() Pick {
	if st := s.cached(); st != nil {
		return Pick{CLI: true, HadFile: true, Token: st.AccessToken(), Upstream: s.cfg.OAuthUpstream, Store: st}
	}
	p := Resolve(s.cfg)
	s.remember(p)
	return p
}

func (s *server) commitStore(store *Store, err error) {
	s.sess = store
	if err == nil {
		if fi, e := os.Stat(store.Path); e == nil {
			s.sessMod = fi.ModTime()
		}
	}
}

func (s *server) refresh(now time.Time) Pick {
	s.mu.Lock()
	for s.refreshing {
		s.cond.Wait()
	}
	p := s.load()
	if p.Store == nil || !p.Store.NeedsRefresh(now) {
		s.mu.Unlock()
		return p
	}
	s.refreshing = true
	store := p.Store
	s.mu.Unlock()

	err := Refresh(context.Background(), store, s.cfg.HTTPClient, s.cfg.TokenURL, now)

	s.mu.Lock()
	s.refreshing = false
	s.cond.Broadcast()
	s.commitStore(store, err)
	applyRefresh(&p, now, err)
	s.mu.Unlock()
	return p
}

func drain(resp *http.Response) int {
	code := resp.StatusCode
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return code
}

func withBearer(req *http.Request, token string) *http.Request {
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+token)
	return req2
}

func (s *server) recoverCLI(resp *http.Response, req *http.Request, store *Store) (*http.Response, int, error) {
	code := drain(resp)
	if store == nil {
		return nil, code, nil
	}
	store.markStale()
	p := s.refresh(time.Now())
	if !retryable(req) || pickStatus(p) != 0 {
		return nil, code, nil
	}
	retry, err := s.doUpstream(withBearer(req, p.Token))
	if err != nil {
		return nil, 0, err
	}
	if !cliRejected(retry) {
		return retry, 0, nil
	}
	return nil, drain(retry), nil
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
	p := s.refresh(time.Now())
	if st := pickStatus(p); st != 0 {
		msg := "run grok login\n"
		if p.Err != nil {
			msg = p.Err.Error() + "\n"
		}
		fail(st, msg)
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
	req, err := s.upstreamRequest(r, p, body, model)
	if err != nil {
		fail(http.StatusBadRequest, "bad request\n")
		return
	}

	resp, err := s.doUpstream(req)
	if err != nil {
		fail(http.StatusBadGateway, "upstream error\n")
		return
	}
	if p.CLI && cliRejected(resp) {
		var code int
		resp, code, err = s.recoverCLI(resp, req, p.Store)
		if err != nil {
			fail(http.StatusBadGateway, "upstream error\n")
			return
		}
		if resp == nil {
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

func (s *server) upstreamRequest(r *http.Request, p Pick, body []byte, model string) (*http.Request, error) {
	upURL := JoinURL(p.Upstream, r.URL.Path)
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
	req.Header.Set("Authorization", "Bearer "+p.Token)
	if p.CLI {
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
	drain(resp)
	time.Sleep(200 * time.Millisecond)
	req2 := req.Clone(req.Context())
	return s.cfg.HTTPClient.Do(req2)
}
