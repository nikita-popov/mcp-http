// mcp-http: HTTP bridge for any stdio MCP server.
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
	port         string        // MCP_HTTP_PORT (default: 8770)
	tokensFile   string        // MCP_HTTP_TOKENS_FILE
	allowedHosts []string      // MCP_HTTP_ALLOWED_HOSTS (comma-separated)
	toolTimeout  time.Duration // MCP_HTTP_TOOL_TIMEOUT (default: 30s)
}

func loadConfig() config {
	cmd := os.Getenv("MCP_HTTP_CMD")
	if cmd == "" {
		log.Fatal("MCP_HTTP_CMD is required")
	}
	port := os.Getenv("MCP_HTTP_PORT")
	if port == "" {
		port = "8770"
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
		port:         port,
		tokensFile:   os.Getenv("MCP_HTTP_TOKENS_FILE"),
		allowedHosts: allowedHosts,
		toolTimeout:  toolTimeout,
	}
}

// loadTokens reads Bearer tokens from a file (one per line, # comments allowed).
func loadTokens(path string) map[string]struct{} {
	tokens := make(map[string]struct{})
	if path == "" {
		return tokens
	}
	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("cannot open tokens file %q: %v", path, err)
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
	return tokens
}

// server wraps the MCP client session and HTTP configuration.
type server struct {
	cfg     config
	tokens  map[string]struct{}
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

func newServer(cfg config) *server {
	tokens := loadTokens(cfg.tokensFile)

	// Parse command: split by spaces to support args in MCP_HTTP_CMD.
	parts := strings.Fields(cfg.cmd)
	cmd := exec.Command(parts[0], parts[1:]...)

	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-http", Version: "v1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		log.Fatalf("cannot connect to MCP server %q: %v", cfg.cmd, err)
	}

	// Collect all tools eagerly for /healthz and validation.
	var tools []*mcp.Tool
	if session.InitializeResult().Capabilities.Tools != nil {
		for t, err := range session.Tools(context.Background(), nil) {
			if err != nil {
				log.Fatalf("list_tools failed: %v", err)
			}
			tools = append(tools, t)
		}
	}
	log.Printf("connected to %q, %d tool(s) available", cfg.cmd, len(tools))

	return &server{cfg: cfg, tokens: tokens, session: session, tools: tools}
}

// authMiddleware enforces Bearer token authentication when tokens are configured.
func (s *server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.tokens) > 0 {
			auth := r.Header.Get("Authorization")
			token := strings.TrimPrefix(auth, "Bearer ")
			if _, ok := s.tokens[token]; !ok {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

// hostMiddleware blocks requests from disallowed Host headers (DNS rebinding protection).
func (s *server) hostMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.allowedHosts) > 0 {
			host := r.Host
			if idx := strings.LastIndex(host, ":"); idx != -1 {
				host = host[:idx]
			}
			allowed := false
			for _, h := range s.cfg.allowedHosts {
				if h == host {
					allowed = true
					break
				}
			}
			if !allowed {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func (s *server) chain(h http.HandlerFunc) http.HandlerFunc {
	return s.hostMiddleware(s.authMiddleware(h))
}

// handleHealthz returns server status and available tools.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	toolNames := make([]string, len(s.tools))
	for i, t := range s.tools {
		toolNames[i] = t.Name
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":    true,
		"cmd":   s.cfg.cmd,
		"tools": toolNames,
	})
}

// callRequest is the JSON body expected by POST /.
type callRequest struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// handleCall proxies a tool call to the upstream MCP server.
func (s *server) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req callRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}
	if req.Tool == "" {
		http.Error(w, "missing \"tool\" field", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.toolTimeout)
	defer cancel()

	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      req.Tool,
		Arguments: req.Arguments,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("call_tool failed: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func main() {
	cfg := loadConfig()
	s := newServer(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.chain(s.handleHealthz))
	mux.HandleFunc("/", s.chain(s.handleCall))

	addr := "127.0.0.1:" + cfg.port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
