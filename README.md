# signal-mcp

An MCP server in Go that lets an MCP client (Claude Desktop, n8n, ...) send and receive Signal messages through
[signal-cli-rest-api](https://github.com/bbernhard/signal-cli-rest-api).

Tools: `send_message_to_user`, `send_message_to_group`, `receive_message` (with timeout). Detailed logging included.

The server was built with Claude Code.

- [`signal-mcp-server/`](signal-mcp-server/README.md): the Go server, its configuration, tools and usage.
- `docker-compose.yaml`: runs `signal-cli-rest-api` and `signal-mcp-server` together.
- `.env.example`: copy to `.env` and set `SIGNAL_NUMBER`.

```bash
cp .env.example .env   # set SIGNAL_NUMBER
docker compose up -d --build
```

Link the Signal account first (QR code via signal-cli-rest-api on port 8080); see the server README.
