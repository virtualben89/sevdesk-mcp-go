# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). Release notes on GitHub are generated from this file.

## [Unreleased]

## [0.1.0] - 2026-11-24

### Added
- First version of the sevDesk MCP server: stdio, Streamable HTTP (`/mcp`) and legacy SSE (`/sse`).
- Read tools for contacts, invoices, credit notes, orders/offers, parts, vouchers, booking accounts,
  bank accounts and transactions; PDF download of invoices, credit notes and orders.
- Write tier (drafts): contacts, addresses, communication ways, invoices, credit notes, orders, parts,
  vouchers incl. bounded file upload, bank transactions.
- Booking tier: payments, enshrine / mark sent / reset / cancel, e-mail dispatch, delete.
- `sevdesk_call` + `sevdesk_list_operations`: every endpoint of sevDesk's official OpenAPI spec (154 operations)
  with per-operation safety classes.
- Dry-run mode, client-side rate limit, 429 backoff, no automatic retry of ambiguous writes.
- Optional multi-tenant mode: clients bring their own sevDesk token (`X-Sevdesk-Token`), sessions bound to both tokens.

### Notes
- Verified end to end against a sevDesk trial account: all read tools, drafts of contacts, addresses,
  communication ways, invoices, orders, credit notes, parts and vouchers (incl. upload), bank transactions,
  booking (mark sent, payment, reset) and multi-tenant HTTP mode. Quirks found on the way are documented in the README.

### Security
- Read-only by default; write and booking tiers are separate opt-in switches and fail closed.
- Redirects are never followed, base URL must be https, path validation against the operation index.
- Fixed before the first release after a full security audit (all with regression tests):
  - `sevdesk_call` `getPdf`: only exactly `preventSendBy=true` counts as read. sevDesk marked drafts as sent
    **and enshrined them** for values like `" true"` or `2`, which were accepted in read-only mode (verified live).
  - `sevdesk_call` export `enshrine`/`enshrineDocuments` flags: arrays and values like `yes`/`on`/`2` no longer
    pass the write tier; every value that is not clearly false needs the booking tier. Flags are judged on the
    exact serialised value that is sent upstream.
  - Multi-tenant mode: legacy SSE silently used the server's sevDesk token instead of the client's
    `X-Sevdesk-Token` (the SDK does not pass request auth to tool calls over SSE). SSE is now disabled whenever
    `SEVDESK_ALLOW_CLIENT_TOKEN=true`.
  - `SEVDESK_DRY_RUN` now also previews GET operations with side effects (`getPdf` without `preventSendBy`,
    send-by-printing, enshrining exports) instead of sending them.
  - HTTP 5xx answers to writes are reported as ambiguous (may have been applied, look it up before retrying).
  - **Behaviour change:** `MCP_ALLOW_NO_AUTH=true` requires `MCP_HTTP_ADDR` on loopback; the Host-header check
    alone does not stop network peers.
- Hardening: `send_document_email` validates `cc`/`bcc` (max. 10 plain addresses); contact `extra` cannot set
  `id`/`objectName`/`mapAll`; `extra` never adds case variants of keys the tool already set; per-token rate
  limiters are evicted least-recently-used instead of being reset wholesale; the upload path is checked for a
  regular file before it is opened; `MaxHeaderBytes` 64 KiB; `.gitignore` covers `secrets/`.

[Unreleased]: https://github.com/virtualben89/sevdesk-mcp-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/virtualben89/sevdesk-mcp-go/releases/tag/v0.1.0
