# Changelog

All notable changes to this project are documented here.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). Release notes on GitHub are generated from this file.

## [Unreleased]

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
