package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/superuserkalo/grok-codex-proxy/proxy"
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

func grokClientVersion() string {
	if v := os.Getenv("GROK_CLIENT_VERSION"); v != "" {
		return v
	}
	b, err := os.ReadFile(filepath.Join(grokHome(), "version.json"))
	if err == nil {
		var ver struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(b, &ver) == nil && ver.Version != "" {
			return ver.Version
		}
	}
	return "1.0.30"
}

func cmdStatus() int {
	path := authPath()
	fmt.Printf("auth_path=%s\n", path)
	s, err := proxy.LoadStore(path)
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
	if !proxy.LoopbackHost(*host) && os.Getenv("PROXY_API_KEY") == "" {
		fmt.Fprintln(os.Stderr, "refusing non-loopback bind without PROXY_API_KEY")
		return 2
	}
	cfg := proxy.Config{
		Host:          *host,
		Port:          *port,
		AuthPath:      authPath(),
		OAuthToken:    os.Getenv("GROK_OAUTH_TOKEN"),
		APIKey:        os.Getenv("XAI_API_KEY"),
		ProxyAPIKey:   os.Getenv("PROXY_API_KEY"),
		ClientVersion: grokClientVersion(),
	}
	cfg.UseCLIHeaders, cfg.Upstream = resolveUpstream(cfg)
	if !*noWrite {
		if err := writeCodexConfig(*host, *port, cfg.ProxyAPIKey != ""); err != nil {
			fmt.Fprintf(os.Stderr, "codex config: %v\n", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "wrote Codex provider table")
	}
	addr := cfg.ListenAddr()
	fmt.Fprintf(os.Stderr, "listening on http://%s\n", addr)
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	if err := http.ListenAndServe(addr, proxy.NewMux(cfg)); err != nil {
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

func resolveUpstream(cfg proxy.Config) (bool, string) {
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
	return proxy.WriteAtomic(path, []byte(proxy.UpsertProvider(src, base, envKey)))
}
