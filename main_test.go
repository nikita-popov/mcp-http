package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- loadConfig ---

func TestLoadConfig_Defaults(t *testing.T) {
	t.Setenv("MCP_HTTP_CMD", "echo")
	t.Setenv("MCP_HTTP_ADDR", "")
	t.Setenv("MCP_HTTP_TOOL_TIMEOUT", "")
	t.Setenv("MCP_HTTP_ALLOWED_HOSTS", "")
	t.Setenv("MCP_HTTP_TOKENS_FILE", "")

	cfg := loadConfig()

	if cfg.cmd != "echo" {
		t.Errorf("cmd: got %q, want %q", cfg.cmd, "echo")
	}
	if cfg.addr != "127.0.0.1:8770" {
		t.Errorf("addr: got %q, want %q", cfg.addr, "127.0.0.1:8770")
	}
	if cfg.toolTimeout != 30*time.Second {
		t.Errorf("toolTimeout: got %v, want 30s", cfg.toolTimeout)
	}
	if len(cfg.allowedHosts) != 0 {
		t.Errorf("allowedHosts: expected empty, got %v", cfg.allowedHosts)
	}
}

func TestLoadConfig_AllEnv(t *testing.T) {
	t.Setenv("MCP_HTTP_CMD", "dav-mcp --verbose")
	t.Setenv("MCP_HTTP_ADDR", "192.168.89.160:9889")
	t.Setenv("MCP_HTTP_TOOL_TIMEOUT", "1m")
	t.Setenv("MCP_HTTP_ALLOWED_HOSTS", "example.com , api.example.com")
	t.Setenv("MCP_HTTP_TOKENS_FILE", "/tmp/tokens")

	cfg := loadConfig()

	if cfg.addr != "192.168.89.160:9889" {
		t.Errorf("addr: got %q", cfg.addr)
	}
	if cfg.toolTimeout != time.Minute {
		t.Errorf("toolTimeout: got %v", cfg.toolTimeout)
	}
	if len(cfg.allowedHosts) != 2 {
		t.Fatalf("allowedHosts: got %v", cfg.allowedHosts)
	}
	if cfg.allowedHosts[0] != "example.com" || cfg.allowedHosts[1] != "api.example.com" {
		t.Errorf("allowedHosts values: %v", cfg.allowedHosts)
	}
	if cfg.tokensFile != "/tmp/tokens" {
		t.Errorf("tokensFile: got %q", cfg.tokensFile)
	}
}

func TestLoadConfig_InvalidTimeout_Fallback(t *testing.T) {
	t.Setenv("MCP_HTTP_CMD", "echo")
	t.Setenv("MCP_HTTP_TOOL_TIMEOUT", "not-a-duration")

	cfg := loadConfig()
	if cfg.toolTimeout != 30*time.Second {
		t.Errorf("expected fallback to 30s, got %v", cfg.toolTimeout)
	}
}

// --- loadTokens ---

func TestLoadTokens_Empty(t *testing.T) {
	tokens := loadTokens("")
	if len(tokens) != 0 {
		t.Errorf("expected empty map for empty path")
	}
}

func TestLoadTokens_File(t *testing.T) {
	f, err := os.CreateTemp("", "tokens-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	f.WriteString("# comment line\n")
	f.WriteString("token-abc\n")
	f.WriteString("  token-xyz  \n")
	f.WriteString("\n")
	f.WriteString("token-123\n")
	f.Close()

	tokens := loadTokens(f.Name())

	for _, want := range []string{"token-abc", "token-xyz", "token-123"} {
		if _, ok := tokens[want]; !ok {
			t.Errorf("token %q not found", want)
		}
	}
	if _, ok := tokens["# comment line"]; ok {
		t.Error("comment line should not be a token")
	}
	if len(tokens) != 3 {
		t.Errorf("expected 3 tokens, got %d", len(tokens))
	}
}

// --- helpers for handler tests ---

func newTestProxy(tokens map[string]struct{}, allowedHosts []string, tools []*mcp.Tool) *proxy {
	return &proxy{
		cfg: config{
			cmd:          "test-cmd",
			addr:         "127.0.0.1:8770",
			allowedHosts: allowedHosts,
			toolTimeout:  5 * time.Second,
		},
		tokens:  tokens,
		session: nil,
		tools:   tools,
	}
}

func noTokens() map[string]struct{} { return map[string]struct{}{} }
func withToken(t string) map[string]struct{} {
	return map[string]struct{}{t: {}}
}

// --- authMiddleware ---

func TestAuthMiddleware_NoTokensConfigured(t *testing.T) {
	p := newTestProxy(noTokens(), nil, nil)
	called := false
	handler := p.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should be called when no tokens configured")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	p := newTestProxy(withToken("secret"), nil, nil)
	called := false
	handler := p.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should be called with valid token")
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	p := newTestProxy(withToken("secret"), nil, nil)
	handler := p.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer wrong")
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	p := newTestProxy(withToken("secret"), nil, nil)
	handler := p.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", rr.Code)
	}
}

// --- hostMiddleware ---

func TestHostMiddleware_NoRestriction(t *testing.T) {
	p := newTestProxy(noTokens(), nil, nil)
	called := false
	handler := p.hostMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "anything.example.com"
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should be called when no allowed hosts configured")
	}
}

func TestHostMiddleware_AllowedHost(t *testing.T) {
	p := newTestProxy(noTokens(), []string{"good.example.com"}, nil)
	called := false
	handler := p.hostMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "good.example.com"
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should be called for allowed host")
	}
}

func TestHostMiddleware_AllowedHostWithPort(t *testing.T) {
	p := newTestProxy(noTokens(), []string{"good.example.com"}, nil)
	called := false
	handler := p.hostMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "good.example.com:8770"
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("handler should strip port and allow the host")
	}
}

func TestHostMiddleware_BlockedHost(t *testing.T) {
	p := newTestProxy(noTokens(), []string{"good.example.com"}, nil)
	handler := p.hostMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.example.com"
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", rr.Code)
	}
}

// --- handleHealthz ---

func TestHandleHealthz(t *testing.T) {
	tools := []*mcp.Tool{
		{Name: "tool_one"},
		{Name: "tool_two"},
	}
	p := newTestProxy(noTokens(), nil, tools)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	p.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("ok: got %v", body["ok"])
	}
	if body["cmd"] != "test-cmd" {
		t.Errorf("cmd: got %v", body["cmd"])
	}
	names, ok := body["tools"].([]any)
	if !ok || len(names) != 2 {
		t.Errorf("tools: got %v", body["tools"])
	}
}
