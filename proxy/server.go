package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log"
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
	ClientVersion string
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

func (cfg Config) ListenAddr() string {
	return net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
}

func LoopbackHost(host string) bool {
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
	if cfg.ProxyAPIKey != "" && r.Header.Get("Authorization") != "Bearer "+cfg.ProxyAPIKey {
		fail(http.StatusUnauthorized, "unauthorized\n")
		return
	}
	token, err := cfg.bearer()
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

	upURL := JoinURL(cfg.Upstream, r.URL.Path)
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
	if cfg.UseCLIHeaders {
		req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		if model != "" {
			req.Header.Set("x-grok-model-override", model)
		}
		ver := cfg.ClientVersion
		if ver == "" {
			ver = "1.0.30"
		}
		req.Header.Set("x-grok-client-version", ver)
	}
	if ae := r.Header.Get("Accept"); ae != "" {
		req.Header.Set("Accept", ae)
	}

	resp, err := cfg.doUpstream(req)
	if err != nil {
		fail(http.StatusBadGateway, "upstream error\n")
		return
	}
	defer resp.Body.Close()

	if cfg.UseCLIHeaders && (resp.StatusCode == 401 || resp.StatusCode == 403) {
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
