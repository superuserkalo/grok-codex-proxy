package proxy

import (
	"bytes"
	"context"
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
	Host          string
	Port          int
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

func (cfg Config) ListenAddr() string {
	return net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
}

func (cfg Config) oauthUp() string {
	if cfg.OAuthUpstream != "" {
		return cfg.OAuthUpstream
	}
	return DefaultOAuthUpstream
}

func (cfg Config) apiUp() string {
	if cfg.APIUpstream != "" {
		return cfg.APIUpstream
	}
	return DefaultAPIUpstream
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
	cfg Config
	mu  sync.Mutex
}

func NewMux(cfg Config) http.Handler {
	s := &server{cfg: cfg}
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

func (s *server) tokenURL() string {
	if s.cfg.TokenURL != "" {
		return s.cfg.TokenURL
	}
	return TokenURL
}

func (s *server) client() *http.Client {
	if s.cfg.HTTPClient != nil {
		return s.cfg.HTTPClient
	}
	return http.DefaultClient
}

func (s *server) bearer(ctx context.Context, now time.Time) (token, upstream string, cli bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := Resolve(s.cfg)
	if p.Store != nil {
		if err := RefreshIfDue(ctx, p.Store, s.client(), s.tokenURL(), now); err != nil {
			return "", p.Upstream, p.CLI, err
		}
		if t := p.Store.AccessToken(); t != "" {
			return t, p.Upstream, p.CLI, nil
		}
		return "", p.Upstream, p.CLI, fmt.Errorf("run grok login")
	}
	return p.Token, p.Upstream, p.CLI, p.Err
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
	token, upstream, cli, err := s.bearer(r.Context(), time.Now())
	if err != nil || token == "" {
		fail(http.StatusUnauthorized, "run grok login\n")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
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
		ver := s.cfg.ClientVersion
		if ver == "" {
			ver = DefaultClientVersion
		}
		req.Header.Set("x-grok-client-version", ver)
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
	idempotent := req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/models")
	if !idempotent || (resp.StatusCode != 429 && resp.StatusCode < 500) {
		return resp, nil
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	time.Sleep(200 * time.Millisecond)
	req2 := req.Clone(req.Context())
	return s.client().Do(req2)
}
