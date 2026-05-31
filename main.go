// mcp-http: MCP Streamable HTTP bridge for any stdio MCP server.
// One process = one upstream stdio MCP server.
// Configure via environment variables (see example.env).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// config holds all runtime configuration read from environment variables.
type config struct {
	cmd          string        // MCP_HTTP_CMD
	addr         string        // MCP_HTTP_ADDR (default: 127.0.0.1:8770)
	tokensFile   string        // MCP_HTTP_TOKENS_FILE
	allowedHosts []string      // MCP_HTTP_ALLOWED_HOSTS (comma-separated)
	toolTimeout  time.Duration // MCP_HTTP_TOOL_TIMEOUT (default: 30s)
}

func loadConfig() config {
	cmd := os.Getenv("MCP_HTTP_CMD")
	if cmd == "" {
		log.Fatal("MCP_HTTP_CMD is required")
	}
	addr := os.Getenv("MCP_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8770"
	}
	toolTimeout := 30 * time.Second
	if v := os.Getenv("MCP_HTTP_TOOL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			toolTimeout = d
		}
	}
	var allowedHosts []string
	if v := os.Getenv("MCP_HTTP_ALLOWED_HOSTS"); v != "" {
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				allowedHosts = append(allowedHosts, h)
			}
		}
	}
	return config{
		cmd:          cmd,
		addr:         addr,
		tokensFile:   os.Getenv("MCP_HTTP_TOKENS_FILE"),
		allowedHosts: allowedHosts,
		toolTimeout:  toolTimeout,
	}
}

// loadTokens reads Bearer tokens from a file (one per line, # comments allowed).
// Returns empty map (auth disabled) when path is empty or file is missing.
func loadTokens(path string) map[string]struct{} {
	tokens := make(map[string]struct{})
	if path == "" {
		log.Print("[auth] no tokens file configured — auth disabled (dev mode)")
		return tokens
	}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("[auth] cannot open tokens file %q — auth disabled: %v", path, err)
		return tokens
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tokens[line] = struct{}{}
	}
	log.Printf("[auth] loaded %d token(s) from %s", len(tokens), path)
	return tokens
}

// proxy holds the upstream MCP client session and discovered tools.
type proxy struct {
	cfg     config
	tokens  map[string]struct{}
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

func newProxy(cfg config) *proxy {
	tokens := loadTokens(cfg.tokensFile)

	parts := strings.Fields(cfg.cmd)
	cmd := exec.Command(parts[0], parts[1:]...)

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-http", Version: "v1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		log.Fatalf("[upstream] cannot connect to %q: %v", cfg.cmd, err)
	}

	var tools []*mcp.Tool
	if session.InitializeResult().Capabilities.Tools != nil {
		for t, err := range session.Tools(context.Background(), nil) {
			if err != nil {
				log.Fatalf("[upstream] list_tools failed: %v", err)
			}
			tools = append(tools, t)
		}
	}
	log.Printf("[upstream] connected to %q, %d tool(s)", cfg.cmd, len(tools))

	return &proxy{cfg: cfg, tokens: tokens, session: session, tools: tools}
}

// buildServer constructs an MCP server that proxies all upstream tools.
func (p *proxy) buildServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mcp-http", Version: "v1.0.0"}, nil)

	for _, t := range p.tools {
		tool := t // capture
		srv.AddTool(tool, func(ctx context.Context, _ *mcp.ServerSession, params *mcp.CallToolParamsFor[map[string]any]) (*mcp.CallToolResult, error) {
			t0 := time.Now()
			ctx, cancel := context.WithTimeout(ctx, p.cfg.toolTimeout)
			defer cancel()

			res, err := p.session.CallTool(ctx, &mcp.CallToolParams{
				Name:      tool.Name,
				Arguments: params.Arguments,
			})
			ms := time.Since(t0).Milliseconds()
			if err != nil {
				log.Printf("[tool] %s → error %dms: %v", tool.Name, ms, err)
				return nil, fmt.Errorf("upstream: %w", err)
			}
			log.Printf("[tool] %s → ok %dms", tool.Name, ms)
			return res, nil
		})
	}

	return srv
}

// authMiddleware enforces Bearer token authentication when tokens are configured.
func (p *proxy) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(p.tokens) > 0 {
			auth := r.Header.Get("Authorization")
			token := strings.TrimPrefix(auth, "Bearer ")
			if _, ok := p.tokens[token]; !ok {
				log.Printf("[auth] 401 invalid/missing token path=%s remote=%s", r.URL.Path, r.RemoteAddr)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hostMiddleware blocks requests from disallowed Host headers (DNS rebinding protection).
func (p *proxy) hostMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(p.cfg.allowedHosts) > 0 {
			host := r.Host
			if idx := strings.LastIndex(host, ":"); idx != -1 {
				host = host[:idx]
			}
			allowed := false
			for _, h := range p.cfg.allowedHosts {
				if h == host {
					allowed = true
					break
				}
			}
			if !allowed {
				log.Printf("[host] 403 blocked host=%q path=%s remote=%s", r.Host, r.URL.Path, r.RemoteAddr)
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealthz returns server status and available tools.
func (p *proxy) handleHealthz(w http.ResponseWriter, r *http.Request) {
	toolNames := make([]string, len(p.tools))
	for i, t := range p.tools {
		toolNames[i] = t.Name
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":    true,
		"cmd":   p.cfg.cmd,
		"tools": toolNames,
	})
}

func main() {
	cfg := loadConfig()
	p := newProxy(cfg)
	srv := p.buildServer()

	// MCP Streamable HTTP handler (standard MCP protocol over HTTP).
	mcpHandler, err := srv.NewStreamableHTTPHandler(context.Background(), nil)
	if err != nil {
		log.Fatalf("cannot create MCP handler: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", p.handleHealthz)
	mux.Handle("/", p.hostMiddleware(p.authMiddleware(mcpHandler)))

	log.Printf("[mcp-http] listening on %s, upstream: %s", cfg.addr, cfg.cmd)
	if err := http.ListenAndServe(cfg.addr, mux); err != nil {
		log.Fatal(err)
	}
}
