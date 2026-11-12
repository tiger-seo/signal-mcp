# signal-mcp-server

A minimal MCP (Model Context Protocol) server, written in Go with **zero
external dependencies** (standard library only), that exposes a
[signal-cli-rest-api](https://github.com/bbernhard/signal-cli-rest-api)
instance as three MCP tools:

| Tool                    | Description                                                |
|--------------------------|-------------------------------------------------------------|
| `send_message_to_user`   | Send a direct message to a Signal user (phone number or username) |
| `send_message_to_group`  | Send a message to a Signal group (by group ID)              |
| `receive_message`        | Long-poll for incoming messages, with a timeout              |

It speaks MCP over **Streamable HTTP** (the current MCP HTTP transport: a
single endpoint, JSON-RPC 2.0 over POST, with optional session tracking via
the `Mcp-Session-Id` header). This is what remote/network-based MCP clients
use, as opposed to the stdio transport used for locally-spawned servers.

## Prerequisites

1. A running `signal-cli-rest-api` instance (default assumed at
   `http://localhost:8080`), with a Signal number already registered or
   linked. See the [project's README](https://github.com/bbernhard/signal-cli-rest-api)
   for how to set that up (Docker is easiest, `MODE=normal` or `MODE=json-rpc`).
2. Go 1.22+ to build this server.

## Build & run

```bash
go build -o signal-mcp-server .
SIGNAL_NUMBER="+15551234567" ./signal-mcp-server
```

By default it listens on `http://127.0.0.1:8081/mcp`, with a plain
`GET /healthz` health check endpoint alongside it.

## Configuration (environment variables)

### Signal / signal-cli-rest-api

| Variable                        | Required | Default                 | Description |
|----------------------------------|----------|--------------------------|--------------|
| `SIGNAL_NUMBER`                  | yes      | -                        | Your registered/linked Signal number, e.g. `+15551234567`. Used both as the sender for outgoing messages and the account polled for incoming ones. |
| `SIGNAL_API_BASE_URL`            | no       | `http://localhost:8080` | Base URL of your `signal-cli-rest-api` instance. |
| `SIGNAL_API_TOKEN`               | no       | -                        | If set, sent as `Authorization: Bearer <token>` to `signal-cli-rest-api` (useful if it sits behind an authenticating reverse proxy — the API itself has no built-in auth). |
| `SIGNAL_HTTP_TIMEOUT_SECONDS`    | no       | `15`                     | Timeout for ordinary (non-`receive`) HTTP calls to `signal-cli-rest-api`. |

### This MCP server's own HTTP transport

| Variable                | Required | Default     | Description |
|---------------------------|----------|-------------|--------------|
| `MCP_HTTP_HOST`          | no       | `127.0.0.1` | Interface to bind. Use `0.0.0.0` to accept non-local connections (then set `MCP_AUTH_TOKEN`!). |
| `MCP_HTTP_PORT`          | no       | `8081`      | Port to bind. |
| `MCP_HTTP_PATH`          | no       | `/mcp`      | The single MCP endpoint path. |
| `MCP_AUTH_TOKEN`         | no       | -           | If set, every request must include `Authorization: Bearer <token>`. **Strongly recommended if binding beyond localhost.** |
| `MCP_ALLOWED_ORIGINS`    | no       | (any)       | Comma-separated list of allowed `Origin` header values, for browser-based clients. Guards against DNS-rebinding attacks. Leave unset for non-browser clients. |
| `MCP_REQUIRE_SESSION`    | no       | `true`      | Enforce the `Mcp-Session-Id` handshake (client must call `initialize` first, then send the returned session id on every later request). Set `false` only for quick manual testing. |
| `MCP_MAX_BODY_BYTES`     | no       | `4194304`   | Max request body size (4 MiB). |

### Logging

| Variable      | Required | Default | Description |
|---------------|----------|---------|--------------|
| `LOG_LEVEL`   | no       | `info`  | `debug` \| `info` \| `warn` \| `error`. `debug` logs full request/response bodies to/from `signal-cli-rest-api`, and every HTTP request this server handles. |
| `LOG_FILE`    | no       | -       | If set, logs are additionally appended to this file (JSON lines). Logs always also go to stderr. |

## Running with Docker

This directory includes a small multi-stage `Dockerfile` (Go build stage →
`alpine:3.20` runtime, non-root user, `HEALTHCHECK` against `/healthz`).

Expected layout, with this directory as a sibling of your `docker-compose.yaml`:

```
project/
├── docker-compose.yaml
├── .env                      # copy from .env.example, fill in SIGNAL_NUMBER
├── signal-cli-config/         # signal-cli-rest-api's persisted session
└── signal-mcp-server/         # this directory (Dockerfile + Go source)
```

```bash
cp .env.example .env   # then edit SIGNAL_NUMBER
docker compose up -d --build
docker compose logs -f signal-mcp-server
```

The compose file wires `signal-mcp-server` onto the same `signal-net` network
as `signal-cli-rest-api` and points `SIGNAL_API_BASE_URL` at it by service
name (`http://signal-cli-rest-api:8080`) — no port needs to be published for
that traffic, it stays inside the Docker network. `signal-mcp-server`'s own
port is published to the host as `127.0.0.1:8081` by default.

Standalone (no compose), from this directory:

```bash
docker build -t signal-mcp-server .
docker run --rm -p 127.0.0.1:8081:8081 \
  -e SIGNAL_NUMBER=+15551234567 \
  -e SIGNAL_API_BASE_URL=http://host.docker.internal:8080 \
  signal-mcp-server
```

## Protocol walkthrough (curl)

```bash
BASE=http://127.0.0.1:8081/mcp

# 1. initialize — capture the session id from the response header
curl -si -X POST "$BASE" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize",
       "params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"curl","version":"1.0"}}}'
# → look for: Mcp-Session-Id: <id>

SESSION=<paste the id here>

# 2. tell the server initialization is complete (a notification — no response body)
curl -s -X POST "$BASE" -H 'Content-Type: application/json' -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

# 3. list tools
curl -s -X POST "$BASE" -H 'Content-Type: application/json' -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 4. call a tool
curl -s -X POST "$BASE" -H 'Content-Type: application/json' -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call",
       "params":{"name":"send_message_to_user",
                 "arguments":{"recipient":"+15559999999","message":"hi!"}}}'

# 5. (optional) terminate the session when done
curl -si -X DELETE "$BASE" -H "Mcp-Session-Id: $SESSION"
```

## Wiring it into an MCP client

Most MCP clients that support remote/HTTP servers ask for a URL rather than a
command to spawn — point yours at:

```
http://127.0.0.1:8081/mcp
```

plus the `Authorization: Bearer <token>` header if you set `MCP_AUTH_TOKEN`.
The exact config field names vary by client and change over time, so check
your specific client's current documentation for how it wants an HTTP-based
MCP server configured.

If you actually need the older **stdio** transport (spawned as a subprocess,
talking JSON-RPC over stdin/stdout) instead of HTTP, that's a different,
simpler transport layer — ask and it can be added back alongside this one.

## Tool details

### `send_message_to_user`
```json
{ "recipient": "+15559999999", "message": "hi!", "text_mode": "normal" }
```
`recipient` may be an E.164 phone number or a Signal username.
`text_mode: "styled"` enables Signal's inline formatting
(`*italic*`, `**bold**`, `~strike~`, `` `mono` ``, `||spoiler||`).

### `send_message_to_group`
```json
{ "group_id": "<base64 group id>", "message": "hi group!" }
```
Get the group ID from `signal-cli-rest-api`'s `GET /v1/groups/{number}` endpoint
(not exposed as a tool here — easy to add if you need it).

### `receive_message`
```json
{ "timeout_seconds": 10, "max_messages": 5, "ignore_attachments": true }
```
Long-polls `GET /v1/receive/{number}` and returns a JSON summary of each
message: `event_type` (`message`/`typing`/`receipt`/`call`/`sync`/`other`),
`source`, `source_name`, `timestamp`, `message` text, `group_id`/`group_name`
if it's a group message, and `attachment_count`.

Because this is a real HTTP request/response cycle, a `receive_message` call
with a large `timeout_seconds` simply keeps the HTTP connection open for that
long before responding — the server places no artificial limit on this
beyond the `timeout_seconds` schema max (300s) and your reverse proxy's own
timeout settings, if any.

Note: `signal-cli-rest-api`'s receive endpoint drains its message queue, so
messages are only ever delivered to **one** caller — keep that in mind if you
have other consumers polling the same account.

## Security notes

- Binds to `127.0.0.1` by default — safe out of the box.
- If you expose it beyond localhost (`MCP_HTTP_HOST=0.0.0.0`, a reverse
  proxy, a tunnel, etc.), **set `MCP_AUTH_TOKEN`**. There's no other
  authentication.
- Put this behind TLS (e.g. a reverse proxy like Caddy/nginx, or a tunnel)
  before exposing it outside a trusted network — this server itself only
  speaks plain HTTP.
- `MCP_ALLOWED_ORIGINS` protects against malicious web pages trying to reach
  this server via a victim's browser (DNS rebinding). Irrelevant for
  non-browser clients.

## Source layout

- `main.go` — entrypoint: config/logging/client wiring, HTTP server lifecycle + graceful shutdown
- `config.go` — environment-variable configuration
- `logging.go` — structured (JSON) logging to stderr (+ optional file)
- `mcp_types.go` — JSON-RPC 2.0 and MCP protocol types
- `mcp_server.go` — transport-agnostic MCP engine: tool registry + method dispatch (`initialize`, `tools/list`, `tools/call`)
- `http_server.go` — the Streamable HTTP transport: routing, sessions, CORS, auth, batching
- `signal_client.go` — HTTP client for `signal-cli-rest-api` (`/v2/send`, `/v1/receive/{number}`)
- `tools.go` — the three tool definitions and their handlers

## Extending

Adding another `signal-cli-rest-api` endpoint (e.g. group management, typing
indicators, reactions) is just: add a method to `SignalClient` in
`signal_client.go`, then register a new tool + handler in `tools.go` following
the existing pattern — no transport-layer changes needed.
