# sevdesk-mcp-go

A [Model Context Protocol](https://modelcontextprotocol.io) server for the **[sevDesk](https://sevdesk.de) accounting API** (read + write), written in Go with the official [go-sdk](https://github.com/modelcontextprotocol/go-sdk). One static binary, a ~10 MB `FROM scratch` image, stdio and HTTP transports.

> **Not affiliated with sevDesk.** Use at your own risk. Read the [security notes](#security-notes) before enabling write access.

## Highlights

- **Safe by default:** read-only out of the box. Writes and "booking" actions are separate, opt-in tiers; there is also a dry-run mode.
- **41 tools** for contacts, invoices, credit notes, orders/offers, parts, vouchers (incl. file upload), booking accounts, bank accounts and transactions, payments, plus a **generic `sevdesk_call`** for every other endpoint of sevDesk's official OpenAPI spec (154 operations indexed).
- **Transports:** stdio, Streamable HTTP (`/mcp`) and legacy SSE (`/sse`). `/healthz` and `/readyz` are public.
- **Auth:** MCP bearer token (rotation list, `_FILE` variant, sessions bound to the token) and optional **multi-tenant mode** where each client brings its own sevDesk token.
- **Hardened:** fail-closed env parsing, no redirects with credentials, path validation against an operation index, client-side rate limit, 429 backoff, no blind retries of writes, bounded uploads/downloads, signed build provenance.

## Safety tiers

| Tier | Env | What becomes available |
|---|---|---|
| **read** (default) | `SEVDESK_READ_ONLY=true` | all `list_*` / `get_*` tools, PDF download, `sevdesk_list_operations`, `sevdesk_call` with GET only |
| **write** | `SEVDESK_READ_ONLY=false` | create/update **drafts**: contacts, addresses, communication ways, invoices, credit notes, orders, parts, vouchers (+ file upload), bank transactions |
| **booking** | additionally `SEVDESK_ALLOW_BOOKING=true` | everything that finalises or removes: `book_payment`, `document_action` (enshrine, mark sent, reset, cancel), `send_document_email`, `delete_object` |
| **dry run** | `SEVDESK_DRY_RUN=true` | writes, and reads with side effects (e.g. `getPdf` without `preventSendBy=true`), return the request that *would* be sent and send nothing; plain reads still run |

- Tools of a disabled tier are **not registered** (the model never sees them).
- `sevdesk_call` classifies every operation (read / write / booking) from its HTTP method and path, and raises the class when the **content** requires it: a document `status` beyond draft in the body, any `enshrine`/`enshrineDocuments` value that is not clearly false (`false`, `0`, empty), or `getPdf` without exactly `preventSendBy=true` (sevDesk marks the document as sent **and enshrines it** for anything else, e.g. `" true"` or `2`). Flags are judged on the exact value that is sent upstream. `sevdesk_list_operations` shows the class of each endpoint.
- Contradicting settings fail at startup instead of being guessed (e.g. `SEVDESK_ALLOW_WRITE=true` with the default `SEVDESK_READ_ONLY=true`). Unparseable booleans fail closed.
- Over HTTP, write tiers require `MCP_AUTH_TOKEN` (no-auth mode is read-only only).

## Configuration

All settings are environment variables (`*_FILE` variants read the value from a file, e.g. Docker/Compose secrets).

### sevDesk

| Variable | Default | Description |
|---|---|---|
| `SEVDESK_API_TOKEN` / `_FILE` | – | sevDesk API token (32 hex characters). **Required**, unless `SEVDESK_ALLOW_CLIENT_TOKEN=true` |
| `SEVDESK_ALLOW_CLIENT_TOKEN` | `false` | Multi-tenant mode: clients may send their own token in the `X-Sevdesk-Token` header (HTTP only, needs `MCP_AUTH_TOKEN`) |
| `SEVDESK_READ_ONLY` | `true` | `false` enables the write tier |
| `SEVDESK_ALLOW_WRITE` | `true` if not read-only | Explicit switch for the write tier (conflicts with `SEVDESK_READ_ONLY=true`) |
| `SEVDESK_ALLOW_BOOKING` | `false` | Enables the booking tier (needs write) |
| `SEVDESK_DRY_RUN` | `false` | Preview writes instead of sending them |
| `SEVDESK_BASE_URL` | `https://my.sevdesk.de/api/v1` | Must be `https` (`http` only for loopback, for tests) |
| `SEVDESK_TIMEOUT` | `30s` | Per request timeout |
| `SEVDESK_RATE_LIMIT` | `5` | Requests per second per token (0 disables) |
| `SEVDESK_UPLOAD_DIR` | – | Directory `upload_voucher_file` may read from (`path` input). Unset = only `content_base64` |
| `SEVDESK_MAX_UPLOAD_BYTES` | `10485760` | Upload size limit (max 32 MiB) |
| `SEVDESK_MAX_DOWNLOAD_BYTES` | `8388608` | PDF download size limit (max 32 MiB) |
| `DEFAULT_LIMIT` | `50` | Default page size of list tools (1-100) |

### MCP server

| Variable / flag | Default | Description |
|---|---|---|
| `MCP_TRANSPORT` / `--transport` | `stdio` | `stdio` or `http` (the Docker image defaults to `http`) |
| `MCP_HTTP_ADDR` | `:8080` | Listen address |
| `MCP_HTTP_PATH` | `/mcp` | Streamable HTTP path |
| `MCP_HTTP_ENABLE_SSE` | `true` | Serve legacy SSE on `/sse`. Always off with `SEVDESK_ALLOW_CLIENT_TOKEN=true` (SSE cannot pass per-client tokens to tool calls) |
| `MCP_HTTP_STATELESS`, `MCP_HTTP_JSON_RESPONSE`, `MCP_HTTP_SESSION_TIMEOUT` | `false`, `false`, `30m` | Streamable HTTP options |
| `MCP_AUTH_TOKEN` / `_FILE` | – | Bearer token(s), min. 16 chars, comma separated for rotation. **HTTP refuses to start without it** unless `MCP_ALLOW_NO_AUTH=true` (read-only only) |
| `MCP_ALLOW_NO_AUTH` | `false` | Explicit opt-out of authentication, read-only only. Requires `MCP_HTTP_ADDR` on loopback (e.g. `127.0.0.1:8080`); only `localhost` + `MCP_ALLOWED_HOSTS` Host headers are accepted. The Host check protects against browsers/DNS rebinding, not against network peers |
| `MCP_ALLOWED_HOSTS` | – | Extra Host header values |
| `LOG_LEVEL`, `LOG_FORMAT` | `info`, `text` | Logs go to stderr; tokens are never logged |

### Multi-tenant mode

With `SEVDESK_ALLOW_CLIENT_TOKEN=true` (+ `MCP_AUTH_TOKEN`), each MCP connection may send `X-Sevdesk-Token: <32 hex>`. That token is used for all sevDesk calls of the connection; without the header the server-side `SEVDESK_API_TOKEN` is used (if set). The MCP bearer is still required. Sessions are bound to *both* tokens: changing either inside a session is answered with 403. A malformed header, or a header sent while the mode is off, is rejected with 400 - it is never ignored silently.

## Tools

| Area | Tools |
|---|---|
| Info | `get_status`, `sevdesk_list_operations` |
| Contacts | `list_contacts`, `get_contact` (+addresses, communication ways), `list_contact_addresses`, `list_communication_ways` |
| Documents | `list_invoices`, `get_invoice`, `list_credit_notes`, `get_credit_note`, `list_orders`, `get_order`, `list_vouchers`, `get_voucher`, `list_document_positions`, `download_document_pdf` |
| Parts | `list_parts`, `get_part`, `get_part_stock` |
| Accounting | `list_accounting_guidance` (booking accounts, tax rules) |
| Banking | `list_check_accounts`, `get_check_account` (+balance), `list_transactions`, `get_transaction` |
| Generic | `sevdesk_call` (any indexed endpoint, tier-checked) |
| Write tier | `create_contact`, `update_contact`, `create_contact_address`, `create_communication_way`, `create_invoice`, `create_credit_note`, `create_order`, `create_part`, `update_part`, `upload_voucher_file`, `create_voucher`, `create_transaction` |
| Booking tier | `book_payment`, `document_action` (`enshrine`, `mark_sent`, `reset_to_open`, `reset_to_draft`, `cancel`), `send_document_email`, `delete_object` |

Design notes:

- List tools are **slim and paginated** (`limit` 1-100, `offset`, `next_offset` in the result); `full=true` returns complete objects.
- Dates are `YYYY-MM-DD` (Europe/Berlin); the server converts them to the formats sevDesk expects.
- `create_invoice`, `create_credit_note`, `create_order` and `create_voucher` **always create drafts**; `extra` can add attributes but cannot change `status`. sevDesk quirks handled: orders and credit notes need an explicit `number`; credit notes (Gutschriften) only accept the tax rules 9/18/19/20 and booking category `PROVISION`; vouchers need `tax_rule_id` and `account_datev_id` from `list_accounting_guidance`.
- `download_document_pdf` always sends `preventSendBy=true`, so fetching a PDF never marks a document as sent.
- `sevdesk_call` only accepts paths found in the embedded operation index (generated from [api.sevdesk.de/openapi.yaml](https://api.sevdesk.de/openapi.yaml), regenerate with `scripts/gen_operations.py`), with numeric ids, no dot segments. Nine well-known read endpoints that the official spec omits (`/SevUser`, `/Category`, `/StaticCountry`, `/Unity`, `/AccountingType`, ...) are included and flagged `supplement`.

## sevDesk quirks (verified against a live trial account)

- IDs and numbers come back as **strings** (`"id":"123"`), and `GET` by id may wrap the object in a list; the tools unwrap it.
- `getPdf?download=true` returns raw PDF bytes, without it JSON with base64 content. The PDF tool uses the JSON form.
- `onlyCredit` / `onlyDebit` of the transaction list are inverted compared to their names. The tool exposes `direction=incoming|outgoing` instead.
- Orders and credit notes need an explicit `number`; credit notes only accept tax rules 9, 18, 19, 20 and a booking category (`PROVISION`).
- Marking an invoice as sent (`sendBy`) **enshrines** it: afterwards it can no longer be reset or deleted.
- `DELETE` on a contact that still has documents answers 200 but deletes nothing. `DELETE /Invoice|/Part|/Voucher/{id}` work although the OpenAPI spec does not list them (indexed as `supplement`).
- Bank transactions can only be created on file-import (CSV/MT940) accounts, not on the default offline account.

## Usage

### Docker

```bash
docker run --rm -p 8080:8080 \
  -e MCP_AUTH_TOKEN="$(openssl rand -hex 24)" \
  -e SEVDESK_API_TOKEN=your32hexcharactertoken000000000 \
  ghcr.io/virtualben89/sevdesk-mcp-go:latest
# MCP endpoint: http://localhost:8080/mcp   (legacy SSE: /sse, probes: /healthz /readyz)
```

With secrets files and write access to a **test account**:

```yaml
services:
  sevdesk-mcp:
    image: ghcr.io/virtualben89/sevdesk-mcp-go:latest
    restart: unless-stopped
    environment:
      MCP_AUTH_TOKEN_FILE: /run/secrets/mcp_token
      SEVDESK_API_TOKEN_FILE: /run/secrets/sevdesk_token
      SEVDESK_READ_ONLY: "false"          # drafts only
      # SEVDESK_ALLOW_BOOKING: "true"     # book, send, enshrine, delete
    secrets: [mcp_token, sevdesk_token]
    ports: ["127.0.0.1:8080:8080"]
secrets:
  mcp_token: { file: ./secrets/mcp_token }
  sevdesk_token: { file: ./secrets/sevdesk_token }
```

### Binary / `go install`

Release binaries (linux, macOS, windows; amd64/arm64) are attached to every GitHub release with checksums and signed provenance. Or build it yourself:

```bash
go install github.com/virtualben89/sevdesk-mcp-go/cmd/sevdesk-mcp@latest
```

### Client configuration

**Claude Desktop / Claude Code (stdio)**

```json
{
  "mcpServers": {
    "sevdesk": {
      "command": "sevdesk-mcp",
      "env": { "SEVDESK_API_TOKEN": "your32hexcharactertoken000000000" }
    }
  }
}
```

Claude Code, remote server: `claude mcp add --transport http sevdesk https://mcp.example.com/mcp --header "Authorization: Bearer $MCP_TOKEN"`

**Cursor** (`~/.cursor/mcp.json`)

```json
{
  "mcpServers": {
    "sevdesk": {
      "url": "https://mcp.example.com/mcp",
      "headers": { "Authorization": "Bearer <MCP_AUTH_TOKEN>" }
    }
  }
}
```

Multi-tenant: add `"X-Sevdesk-Token": "<your own sevDesk token>"` to `headers`.

**OpenAI Codex** (`~/.codex/config.toml`)

```toml
[mcp_servers.sevdesk]
command = "sevdesk-mcp"
env = { SEVDESK_API_TOKEN = "your32hexcharactertoken000000000" }

# or remote:
# [mcp_servers.sevdesk]
# url = "https://mcp.example.com/mcp"
# bearer_token_env_var = "SEVDESK_MCP_TOKEN"
```

**Hermes Agent** (`~/.hermes/config.yaml`)

```yaml
mcp_servers:
  sevdesk:
    url: https://mcp.example.com/mcp
    headers:
      Authorization: "Bearer <MCP_AUTH_TOKEN>"
```

## Security notes

- **A sevDesk API token has full access to the whole account.** There are no scopes, it does not expire, and there is no sandbox. Anyone holding the token (and anyone who can reach this server with the MCP token) can read all bookkeeping data and, with write tiers, change it.
- Use a **dedicated sevDesk user** for the integration, so the token can be rotated or revoked without touching a person's login and actions show up under a recognisable name.
- Try write features on a **sevDesk trial account** first. Keep `SEVDESK_READ_ONLY=true` for production unless you need writes; enable the booking tier only deliberately. Several actions (enshrine, sending invoices) are irreversible in sevDesk.
- Keep tokens in `*_FILE` secrets, not in command lines. The server never logs tokens, and transport errors are stripped of URLs.
- Put HTTP behind TLS (reverse proxy). The MCP bearer is compared in constant time; with `MCP_AUTH_TOKEN` set, sessions are bound to it. SSE sessions are pinned by this server. A write that fails with a transport error or HTTP 5xx is reported as *ambiguous* (it may have been applied) and never retried: look the object up before trying again.
- The sevDesk token is sent as the raw `Authorization` header to the configured base URL only; redirects are never followed.
- Prompt injection is real: sevDesk data (contact names, notes, document texts) is untrusted input to the model. Do not combine write tiers with untrusted data sources unless a human confirms the tool calls.

## Development

```bash
go test -race ./...
go run honnef.co/go/tools/cmd/staticcheck@latest ./...
python3 scripts/gen_operations.py            # refresh the operation index from sevDesk's OpenAPI spec (needs PyYAML)
python3 scripts/smoke.py --help              # stdio smoke test against a real (trial!) account
```

Unit tests use `httptest` fakes. `scripts/smoke.py` runs the real binary over stdio against sevDesk; with `--write` it also creates and cleans up test data, so **only use it with a trial account**.

## License

MIT, see [LICENSE](LICENSE).
