#!/usr/bin/env python3
"""Generate internal/sevdesk/operations.json from sevDesk's official OpenAPI spec.

Usage: python3 scripts/gen_operations.py [openapi.yaml]   (needs PyYAML)
Without an argument the spec is downloaded from https://api.sevdesk.de/openapi.yaml.
The result is committed; the server embeds it and never reads the spec at runtime.
"""
import json
import sys
import urllib.request

import yaml

SPEC_URL = "https://api.sevdesk.de/openapi.yaml"
OUT = "internal/sevdesk/operations.json"


def load(path):
    if path:
        return yaml.safe_load(open(path, encoding="utf-8"))
    with urllib.request.urlopen(SPEC_URL, timeout=60) as r:
        return yaml.safe_load(r.read().decode("utf-8"))


def deref(spec, node):
    while isinstance(node, dict) and "$ref" in node:
        cur = spec
        for part in node["$ref"].lstrip("#/").split("/"):
            cur = cur[part]
        node = cur
    return node


def main():
    spec = load(sys.argv[1] if len(sys.argv) > 1 else None)
    ops = []
    for path, item in spec["paths"].items():
        shared = item.get("parameters", [])
        for method, op in item.items():
            if method not in ("get", "post", "put", "delete", "patch"):
                continue
            params = [deref(spec, p) for p in shared + op.get("parameters", [])]
            body = op.get("requestBody") and deref(spec, op["requestBody"])
            ctypes = list((body or {}).get("content", {}).keys())
            ops.append({
                "m": method.upper(),
                "p": path,
                "id": op.get("operationId", ""),
                "t": (op.get("tags") or [""])[0],
                "s": " ".join((op.get("summary") or "").split())[:100],
                "q": sorted(p["name"] for p in params if p.get("in") == "query"),
                "b": any("json" in c or "form-urlencoded" in c for c in ctypes),
                "mp": any("multipart" in c or c == "form-data" for c in ctypes),
            })
    ops.sort(key=lambda o: (o["p"], o["m"]))
    out = {"spec_version": spec["info"].get("version", ""), "operations": ops}
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, separators=(",", ":"))
        f.write("\n")
    print(f"{len(ops)} operations, spec version {out['spec_version']} -> {OUT}")


if __name__ == "__main__":
    main()
