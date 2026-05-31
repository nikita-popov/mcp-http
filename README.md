# mcp-http

HTTP bridge for any [stdio MCP](https://modelcontextprotocol.io/) server.

One process = one upstream stdio MCP server.  
Multiple servers → multiple instances via the `mcp-http@<name>.service` systemd template.

## How it works

```
HTTP client  ──POST /──►  mcp-http  ──stdio──►  any MCP server
             ◄─JSON───              ◄────────
```

`mcp-http` connects to the upstream server at startup, discovers all tools via
`list_tools`, and proxies `call_tool` requests over HTTP.

## API

### `GET /healthz`

Returns server status and list of available tools.

```json
{"ok": true, "cmd": "dav-mcp", "tools": ["read_file", "write_file"]}
```

### `POST /`

Call a tool on the upstream MCP server.

```json
{"tool": "read_file", "arguments": {"path": "/etc/hosts"}}
```

Returns the MCP `CallToolResult` as JSON.

If `MCP_HTTP_TOKENS_FILE` is set, the request must include:
```
Authorization: Bearer <token>
```

## Configuration

All configuration is via environment variables (see `example.env`):

| Variable | Required | Default | Description |
|---|---|---|---|
| `MCP_HTTP_CMD` | ✓ | — | Command to launch the upstream MCP server |
| `MCP_HTTP_ADDR` | | `127.0.0.1:8770` | Listen address (`host:port`) |
| `MCP_HTTP_TOKENS_FILE` | | — | Path to Bearer tokens file (one per line) |
| `MCP_HTTP_ALLOWED_HOSTS` | | — | Comma-separated allowed Host headers |
| `MCP_HTTP_TOOL_TIMEOUT` | | `30s` | Per-call timeout (Go duration) |

## Install

```bash
go install github.com/nikita-popov/mcp-http@latest
```

Or build from source:

```bash
git clone https://github.com/nikita-popov/mcp-http
cd mcp-http
go build -o mcp-http .
sudo mv mcp-http /usr/local/bin/
```

## systemd (multi-instance)

Copy the template unit:

```bash
sudo cp example@.service /etc/systemd/system/mcp-http@.service
sudo systemctl daemon-reload
```

Create a config for each instance:

```bash
# /etc/mcp-http/dav-mcp.env
MCP_HTTP_CMD=dav-mcp
MCP_HTTP_ADDR=127.0.0.1:8771
MCP_HTTP_TOKENS_FILE=/etc/mcp-http/dav-mcp.tokens
```

Enable and start:

```bash
sudo systemctl enable --now mcp-http@dav-mcp
sudo systemctl enable --now mcp-http@mempalace
sudo journalctl -u mcp-http@dav-mcp -f
```

See `example.nginx.conf` for a reverse proxy configuration.

## License

MIT
