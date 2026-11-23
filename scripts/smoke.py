#!/usr/bin/env python3
"""stdio smoke test for sevdesk-mcp against a real sevDesk account.

  SEVDESK_API_TOKEN=... python3 scripts/smoke.py ./bin/sevdesk-mcp            # read-only checks
  python3 scripts/smoke.py ./bin/sevdesk-mcp --env-file ~/.config/sevdesk-mcp/test.env --write

--write ALSO CREATES test data (contact, address, invoice draft, part, voucher draft) and deletes
what the API allows. Use it ONLY with a sevDesk trial/test account, never with production data.
--base-url lets you point the binary at a mock (loopback http only).
Exit code 0 = all checks passed.
"""
import argparse
import base64
import json
import os
import subprocess
import sys
import time

ap = argparse.ArgumentParser()
ap.add_argument("binary")
ap.add_argument("--env-file", help="file with KEY=VALUE lines (e.g. SEVDESK_API_TOKEN=...)")
ap.add_argument("--write", action="store_true", help="also run write tests (TRIAL ACCOUNT ONLY)")
ap.add_argument("--base-url")
args = ap.parse_args()

env = dict(os.environ)
if args.env_file:
    for line in open(os.path.expanduser(args.env_file), encoding="utf-8"):
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            k, v = line.split("=", 1)
            env[k.strip()] = v.strip().strip('"').strip("'")
if args.base_url:
    env["SEVDESK_BASE_URL"] = args.base_url
env["MCP_TRANSPORT"] = "stdio"
if args.write:
    env["SEVDESK_READ_ONLY"] = "false"
    env["SEVDESK_ALLOW_BOOKING"] = "true"  # needed for cleanup (delete_object)
else:
    env["SEVDESK_READ_ONLY"] = "true"
    env.pop("SEVDESK_ALLOW_BOOKING", None)
for k in ("SEVDESK_ALLOW_WRITE", "SEVDESK_DRY_RUN", "SEVDESK_ALLOW_CLIENT_TOKEN"):
    env.pop(k, None)

proc = subprocess.Popen([args.binary], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                        env=env, text=True, bufsize=1)
_id = 0
failures = []


def rpc(method, params=None, notify=False):
    global _id
    msg = {"jsonrpc": "2.0", "method": method}
    if params is not None:
        msg["params"] = params
    if not notify:
        _id += 1
        msg["id"] = _id
    proc.stdin.write(json.dumps(msg) + "\n")
    proc.stdin.flush()
    if notify:
        return None
    while True:
        line = proc.stdout.readline()
        if not line:
            raise SystemExit("server closed stdout; stderr:\n" + proc.stderr.read())
        resp = json.loads(line)
        if resp.get("id") == _id:
            return resp


def tool(name, arguments=None):
    r = rpc("tools/call", {"name": name, "arguments": arguments or {}})
    if "error" in r:
        return True, json.dumps(r["error"])
    res = r["result"]
    text = res["content"][0]["text"] if res.get("content") else ""
    return bool(res.get("isError")), text


def check(label, ok, detail=""):
    print(("PASS " if ok else "FAIL ") + label + ("" if ok else "  -> " + detail[:300]))
    if not ok:
        failures.append(label)


def js(text):
    try:
        return json.loads(text)
    except Exception:
        return {}


def first_id(listing):
    items = js(listing).get("items") or []
    return items[0].get("id") if items else None


r = rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1"}})
check("initialize", "result" in r, json.dumps(r))
rpc("notifications/initialized", notify=True)
tools = {t["name"]: t for t in rpc("tools/list")["result"]["tools"]}
print(f"{len(tools)} tools")
check("tools/list has core tools", {"get_status", "list_invoices", "sevdesk_call"} <= set(tools))
check("write tools hidden in read-only mode" if not args.write else "write tools visible", ("create_invoice" in tools) == args.write)

err, out = tool("get_status")
check("get_status reaches sevDesk", not err and js(out).get("sevdesk_reachable") is True, out)

reads = [
    ("list_contacts", {"limit": 5}), ("list_invoices", {"limit": 5}), ("list_credit_notes", {"limit": 5}),
    ("list_orders", {"limit": 5}), ("list_vouchers", {"limit": 5}), ("list_parts", {"limit": 5}),
    ("list_check_accounts", {}), ("list_transactions", {"limit": 5}), ("list_accounting_guidance", {"kind": "expense"}),
    ("sevdesk_list_operations", {"search": "Invoice", "limit": 5}),
    ("sevdesk_call", {"method": "GET", "path": "/SevUser"}),
    ("sevdesk_call", {"method": "GET", "path": "/Category", "query": {"objectType": "Contact"}}),
    ("sevdesk_call", {"method": "GET", "path": "/Unity"}),
    ("sevdesk_call", {"method": "GET", "path": "/StaticCountry", "query": {"limit": 3}}),
    ("sevdesk_call", {"method": "GET", "path": "/CommunicationWayKey"}),
    ("sevdesk_call", {"method": "GET", "path": "/Tools/bookkeepingSystemVersion"}),
]
outs = {}
for name, a in reads:
    err, out = tool(name, a)
    outs[(name, json.dumps(a, sort_keys=True))] = out
    check(f"{name} {json.dumps(a)}", not err, out)
    time.sleep(0.25)

for lst, get in (("list_contacts", "get_contact"), ("list_invoices", "get_invoice"), ("list_parts", "get_part"),
                 ("list_vouchers", "get_voucher"), ("list_orders", "get_order"), ("list_check_accounts", "get_check_account")):
    _, listing = tool(lst, {"limit": 1})
    i = first_id(listing)
    if i:
        err, out = tool(get, {"id": int(i)})
        check(f"{get} {i}", not err, out)
    else:
        print(f"SKIP {get}: no data")
_, listing = tool("list_invoices", {"limit": 1})
inv = first_id(listing)
if inv:
    err, out = tool("download_document_pdf", {"document_type": "invoice", "id": int(inv)})
    check("download_document_pdf", not err and "content_base64" in out, out)
    err, out = tool("list_document_positions", {"document_type": "invoice", "id": int(inv)})
    check("list_document_positions", not err, out)

# the safety net must hold in every mode
err, out = tool("sevdesk_call", {"method": "DELETE", "path": "/Contact/1"})
check("DELETE refused unless booking tier", err if not args.write else True, out)

if args.write:
    print("--- write tests (trial account only!) ---")
    leftovers = []
    err, out = tool("create_contact", {"name": "MCP-Smoke GmbH", "description": "created by sevdesk-mcp smoke test"})
    check("create_contact", not err, out)
    res = js(out).get("result")
    cid = int((res[0] if isinstance(res, list) else res or {}).get("id", 0) or 0)
    if cid:
        _, cats = tool("sevdesk_call", {"method": "GET", "path": "/Category", "query": {"objectType": "ContactAddress"}})
        catid = next(iter((js(cats).get("result", {}).get("objects") or [{}])), {}).get("id")
        if catid:
            err, out = tool("create_contact_address", {"contact_id": cid, "street": "Teststr. 1", "zip": "10115", "city": "Berlin", "category_id": int(catid)})
            check("create_contact_address", not err, out)
        _, keys = tool("sevdesk_call", {"method": "GET", "path": "/CommunicationWayKey"})
        kid = next(iter((js(keys).get("result", {}).get("objects") or [{}])), {}).get("id")
        if kid:
            err, out = tool("create_communication_way", {"contact_id": cid, "type": "EMAIL", "value": "smoke@example.com", "key_id": int(kid)})
            check("create_communication_way", not err, out)
        err, out = tool("get_contact", {"id": cid, "include_addresses": True, "include_communication_ways": True})
        check("get_contact incl. children", not err and "MCP-Smoke" in out, out)

        err, out = tool("create_invoice", {"contact_id": cid, "date": time.strftime("%Y-%m-%d"), "header": "MCP smoke draft",
                                           "positions": [{"name": "Smoke test", "quantity": 1, "price": 10, "tax_rate": 19}]})
        check("create_invoice (draft)", not err, out)
        if not err:
            r2 = js(out).get("result", {})
            iid = int(((r2.get("invoice") if isinstance(r2, dict) else None) or {}).get("id", 0) or 0)
            if iid:
                err, out = tool("get_invoice", {"id": iid})
                check("get_invoice(draft) status draft", not err and '"100"' in out.replace(" ", ""), out)
                err, out = tool("list_document_positions", {"document_type": "invoice", "id": iid})
                check("positions of new invoice", not err and "Smoke test" in out, out)
                err, out = tool("download_document_pdf", {"document_type": "invoice", "id": iid})
                check("download_document_pdf of a draft", not err and "JVBER" in out, out)
                err, out = tool("delete_object", {"entity": "invoice", "id": iid})
                check("delete_object invoice draft (cleanup)", not err, out)

        err, out = tool("create_order", {"contact_id": cid, "date": time.strftime("%Y-%m-%d"), "header": "MCP smoke offer", "number": "AN-MCP-%d" % int(time.time()),
                                         "positions": [{"name": "Smoke offer", "quantity": 1, "price": 5, "tax_rate": 19}]})
        check("create_order (draft offer)", not err, out)
        r3 = js(out).get("result", {})
        oid = int(((r3.get("order") if isinstance(r3, dict) else None) or {}).get("id", 0) or 0)
        if oid:
            err, out = tool("delete_object", {"entity": "order", "id": oid})
            check("delete_object order (cleanup)", not err, out)
        err, out = tool("create_credit_note", {"contact_id": cid, "date": time.strftime("%Y-%m-%d"), "header": "MCP smoke credit note", "number": "GS-MCP-%d" % int(time.time()),
                                               "positions": [{"name": "Smoke credit", "quantity": 1, "price": 3, "tax_rate": 19}]})
        check("create_credit_note (draft)", not err, out)
        r5 = js(out).get("result", {})
        gid = int(((r5.get("creditNote") if isinstance(r5, dict) else None) or {}).get("id", 0) or 0)
        if gid:
            err, out = tool("delete_object", {"entity": "credit_note", "id": gid})
            check("delete_object credit note (cleanup)", not err, out)

    err, out = tool("create_part", {"name": "MCP-Smoke Part", "part_number": "MCP-%d" % int(time.time()), "price_net": 1, "tax_rate": 19})
    check("create_part", not err, out)
    r4 = js(out).get("result")
    pid = int((r4[0] if isinstance(r4, list) else r4 or {}).get("id", 0) or 0) if not err else 0
    if pid:
        err, out = tool("get_part_stock", {"id": pid})
        check("get_part_stock", not err, out)
        err, out = tool("delete_object", {"entity": "part", "id": pid})
        check("delete_object part (cleanup)", not err, out)

    # voucher with file
    pdf = b"%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n"
    err, out = tool("upload_voucher_file", {"content_base64": base64.b64encode(pdf).decode(), "filename": "smoke.pdf"})
    check("upload_voucher_file", not err, out)
    fname = (js(out).get("result") or {}).get("filename") if not err else None
    _, g = tool("list_accounting_guidance", {"kind": "expense", "search": "Bürobedarf", "limit": 1})
    gi = (js(g).get("items") or [None])[0]
    vid = None
    if gi and gi.get("allowedTaxRules"):
        rule = next((r["id"] for r in gi["allowedTaxRules"] if "NINETEEN" in (r.get("taxRates") or [])), gi["allowedTaxRules"][0]["id"])
        va = {"credit_debit": "D", "voucher_date": time.strftime("%Y-%m-%d"), "supplier_name": "MCP-Smoke Lieferant",
              "description": "MCP smoke voucher", "tax_rule_id": int(rule),
              "positions": [{"account_datev_id": int(gi["accountDatevId"]), "tax_rate": 19, "amount": 11.9}]}
        if fname:
            va["filename"] = fname
        err, out = tool("create_voucher", va)
        check("create_voucher (draft)", not err, out)
        vid = int(((js(out).get("result") or {}).get("voucher") or {}).get("id", 0) or 0)
        if vid:
            err, out = tool("get_voucher", {"id": vid})
            check("get_voucher(draft) status 50", not err and '"50"' in out.replace(" ", ""), out)
            err, out = tool("list_document_positions", {"document_type": "voucher", "id": vid})
            check("voucher positions", not err, out)
            err, out = tool("delete_object", {"entity": "voucher", "id": vid})
            check("delete_object voucher (cleanup)", not err, out)
    else:
        check("booking account found via list_accounting_guidance", False, g)

    if cid:
        err, out = tool("delete_object", {"entity": "contact", "id": cid})
        check("delete_object contact (cleanup)", not err, out)
        err, out = tool("get_contact", {"id": cid})
        check("deleted contact is gone (404)", err and "404" in out, out)
    if leftovers:
        print("LEFTOVERS in the trial account:")
        for l in leftovers:
            print("  -", l)

proc.stdin.close()
try:
    proc.wait(timeout=5)
except subprocess.TimeoutExpired:
    proc.kill()
if failures:
    print(f"\n{len(failures)} check(s) FAILED")
    sys.exit(1)
print("\nall checks passed")
