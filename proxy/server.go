package proxy

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
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
	cfg  Config
	sess *session
}

func NewMux(cfg Config) http.Handler {
	cfg = cfg.prepared()
	s := &server{cfg: cfg, sess: newSession(cfg)}
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
	p := s.sess.live(time.Now())
	if st, msg := p.gate(); st != 0 {
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
	if p.CLI() && cliRejected(resp) {
		code := drain(resp)
		if p.Store != nil {
			p = s.sess.rotate(time.Now())
		}
		if !retryable(req) || p.Store == nil {
			fail(code, cliTokenRejected)
			return
		}
		if st, _ := p.gate(); st != 0 {
			fail(code, cliTokenRejected)
			return
		}
		resp, err = s.doUpstream(withBearer(req, p.Token))
		if err != nil {
			fail(http.StatusBadGateway, "upstream error\n")
			return
		}
		if cliRejected(resp) {
			fail(drain(resp), cliTokenRejected)
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
	if p.CLI() {
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
