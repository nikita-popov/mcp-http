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

func newTestServer(tokens map[string]struct{}, allowedHosts []string, tools []*mcp.Tool) *server {
	return &server{
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
	s := newTestServer(noTokens(), nil, nil)
	called := false
	handler := s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler(rr, req)

	if !called {
		t.Error("handler should be called when no tokens configured")
	}
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	s := newTestServer(withToken("secret"), nil, nil)
	called := false
	handler := s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	handler(rr, req)

	if !called {
		t.Error("handler should be called with valid token")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	s := newTestServer(withToken("secret"), nil, nil)
	handler := s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	s := newTestServer(withToken("secret"), nil, nil)
	handler := s.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rr.Code)
	}
}

// --- hostMiddleware ---

func TestHostMiddleware_NoRestriction(t *testing.T) {
	s := newTestServer(noTokens(), nil, nil)
	called := false
	handler := s.hostMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "anything.example.com"
	handler(rr, req)

	if !called {
		t.Error("handler should be called when no allowedHosts configured")
	}
}

func TestHostMiddleware_AllowedHost(t *testing.T) {
	s := newTestServer(noTokens(), []string{"example.com"}, nil)
	called := false
	handler := s.hostMiddleware(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	for _, host := range []string{"example.com", "example.com:443"} {
		called = false
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		handler(rr, req)
		if !called {
			t.Errorf("host %q should be allowed", host)
		}
	}
}

func TestHostMiddleware_ForbiddenHost(t *testing.T) {
	s := newTestServer(noTokens(), []string{"example.com"}, nil)
	handler := s.hostMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "evil.com"
	handler(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

// --- handleHealthz ---

func TestHandleHealthz(t *testing.T) {
	tools := []*mcp.Tool{
		{Name: "read_file"},
		{Name: "write_file"},
	}
	s := newTestServer(noTokens(), nil, tools)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q", ct)
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("ok: got %v", resp["ok"])
	}
	if resp["cmd"] != "test-cmd" {
		t.Errorf("cmd: got %v", resp["cmd"])
	}
	names, ok := resp["tools"].([]any)
	if !ok || len(names) != 2 {
		t.Fatalf("tools: got %v", resp["tools"])
	}
	if names[0] != "read_file" || names[1] != "write_file" {
		t.Errorf("tool names: %v", names)
	}
}

func TestHandleHealthz_NoTools(t *testing.T) {
	s := newTestServer(noTokens(), nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.handleHealthz(rr, req)

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	names := resp["tools"].([]any)
	if len(names) != 0 {
		t.Errorf("expected empty tools, got %v", names)
	}
}

// --- handleCall: validation only (no real MCP session) ---

func TestHandleCall_MethodNotAllowed(t *testing.T) {
	s := newTestServer(noTokens(), nil, nil)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/", nil)
		s.handleCall(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: expected 405, got %d", method, rr.Code)
		}
	}
}

func TestHandleCall_InvalidJSON(t *testing.T) {
	s := newTestServer(noTokens(), nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not-json"))
	s.handleCall(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestHandleCall_MissingToolField(t *testing.T) {
	s := newTestServer(noTokens(), nil, nil)
	rr := httptest.NewRecorder()
	body := `{"arguments": {}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	s.handleCall(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "tool") {
		t.Errorf("error message should mention 'tool': %q", rr.Body.String())
	}
}
