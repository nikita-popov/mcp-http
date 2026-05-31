# mcp-http

Minimal MCP Streamable HTTP bridge. Wraps **any** local MCP stdio server and
exposes it over HTTP with Bearer-token auth.

Single file (`server.py`), no FastAPI. One process = one upstream server.
For multiple servers — use the systemd template `mcp-http@.service`.

## How it works

```
MCP client (HTTPS)
      │  Bearer token
      ▼
  mcp-http (uvicorn)
      │  MCP stdio subprocess
      ▼
  any-mcp-server (stdio)
```

On startup mcp-http:
1. Spawns the upstream MCP stdio server as a subprocess (`MCPClient`)
2. Calls `list_tools()` to discover all available tools
3. Registers every discovered tool as a passthrough FastMCP tool
4. Serves them over Streamable HTTP with Bearer-token auth

No hardcoded tool list. Adding tools to the upstream server = they appear automatically.

## Endpoints

| Path | Auth | Description |
|---|---|---|
| `GET /healthz` | — | Health check + tool list |
| `POST /` | Bearer | MCP Streamable HTTP |

`/healthz` response:
```json
{"ok": true, "cmd": "dav-mcp", "tools": 8, "tool_names": ["..."]} 
```

## Install

```bash
useradd -r -s /usr/sbin/nologin mcp-http
mkdir -p /opt/mcp-http /etc/mcp-http
python3 -m venv /opt/mcp-http/venv
/opt/mcp-http/venv/bin/pip install -r requirements.txt
cp server.py /opt/mcp-http/
```

The upstream MCP server (e.g. `dav-mcp`) must be installed separately and
accessible via `MCP_HTTP_CMD`.

## Config

See `example.env` for all variables. Key ones:

```env
# Command to launch the upstream MCP stdio server
MCP_HTTP_CMD=/opt/dav-mcp/dav-mcp

# Port this instance listens on
MCP_HTTP_PORT=8771

# Bearer tokens file (one token per line)
MCP_HTTP_TOKENS_FILE=/etc/mcp-http/dav-mcp.tokens

# Allowed external hostnames (DNS rebinding protection)
MCP_HTTP_ALLOWED_HOSTS=your-domain.example.com
```

Generate a token:
```bash
python3 -c "import secrets; print(secrets.token_urlsafe(32))"
```

## Single instance (mcp-http.service)

```bash
cp example.service /etc/systemd/system/mcp-http.service
cp example.env /etc/mcp-http/env
# edit /etc/mcp-http/env
systemctl enable --now mcp-http
```

## Multiple instances (mcp-http@.service)

One systemd template unit, one env file per upstream server:

```bash
cp example@.service /etc/systemd/system/mcp-http@.service
systemctl daemon-reload

# Instance: dav-mcp on port 8771
cp example.env /etc/mcp-http/dav-mcp.env
# edit /etc/mcp-http/dav-mcp.env — set MCP_HTTP_CMD, MCP_HTTP_PORT, etc.
systemctl enable --now mcp-http@dav-mcp

# Instance: mempalace on port 8772
cp example.env /etc/mcp-http/mempalace.env
# edit /etc/mcp-http/mempalace.env
systemctl enable --now mcp-http@mempalace

# Manage
systemctl status 'mcp-http@*'
journalctl -u mcp-http@dav-mcp -f
```

### nginx (multi-instance example)

```nginx
# mcp-http@dav-mcp  → /dav/
location /dav/ {
    proxy_pass http://127.0.0.1:8771/;
    proxy_buffering off;
    proxy_read_timeout 300s;
}

# mcp-http@mempalace → /mem/
location /mem/ {
    proxy_pass http://127.0.0.1:8772/;
    proxy_buffering off;
    proxy_read_timeout 300s;
}
```

See `example.nginx.conf` for the full location block template.

## MCP connector

- **URL**: `https://your-domain/mcp/` (or per-instance path)
- **Transport**: Streamable HTTP
- **Auth**: Bearer token from tokens file
