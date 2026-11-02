#!/usr/bin/python3 -I
"""Partial client update check, run on the server against the panel that is installed now.

1. Read client EMAIL (GET /panel/api/clients/get/:email).
2. Give it non-default settings with a full update (the whole client as read, plus the settings).
3. Send a partial update: only {"email", "expiryTime"} (a bot renewing a subscription).
4. Read it back and print every field the partial update changed besides expiryTime.

usage: partial-update.py BASE_URL EMAIL     (token from `x-ui setting -getApiToken`)
"""
import json
import os
import subprocess
import sys
import urllib.request

base, email = sys.argv[1].rstrip("/"), sys.argv[2]
env = dict(os.environ, XUI_DB_FOLDER="/root/still-bench/db")
out = subprocess.run(["/usr/local/x-ui/x-ui", "setting", "-getApiToken"], capture_output=True, text=True,
                     cwd="/usr/local/x-ui", env=env).stdout
token = out.split("apiToken:", 1)[1].strip().split()[0]


def call(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + token)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=30) as resp:
        res = json.loads(resp.read())
    if not res.get("success"):
        raise SystemExit("%s %s: %s" % (method, path, res.get("msg")))
    return res.get("obj")


def get():
    obj = call("GET", "/panel/api/clients/get/" + email)
    return obj.get("client", obj) if isinstance(obj, dict) else obj


SECRET = {"privateKey", "preSharedKey", "publicKey", "id", "password", "auth", "subId"}
before = get()
# The read model carries the database row id as "id"; the update body's "id" is the client UUID.
full = {k: v for k, v in before.items() if k not in ("id", "createdAt", "updatedAt")}
if before.get("uuid"):
    full["id"] = before["uuid"]
# The read model joins allowedIPs into one string; the update body takes a list.
if isinstance(full.get("allowedIPs"), str):
    full["allowedIPs"] = [s.strip() for s in full["allowedIPs"].split(",") if s.strip()]
full.update({"enable": True, "limitIp": 3, "totalGB": 50 * 1024 ** 3, "tgId": 123456789,
             "comment": "bench", "reset": 30})
call("POST", "/panel/api/clients/update/" + email, full)
prepared = get()
new_expiry = int(prepared.get("expiryTime") or 0) + 30 * 86400 * 1000
call("POST", "/panel/api/clients/update/" + email, {"email": email, "expiryTime": new_expiry})
after = get()

changed = []
for k in sorted(set(prepared) | set(after)):
    if k in ("expiryTime", "updatedAt", "lastOnline"):
        continue
    if prepared.get(k) != after.get(k):
        show = (lambda v: "<changed>" if k in SECRET else json.dumps(v))
        changed.append("%s: %s -> %s" % (k, show(prepared.get(k)), show(after.get(k))))
print("panel:", subprocess.run(["/usr/local/x-ui/x-ui", "-v"], capture_output=True, text=True).stdout.strip())
print("expiryTime applied:", after.get("expiryTime") == new_expiry)
print("fields changed by the partial update (besides expiryTime): %d" % len(changed))
for c in changed:
    print("  " + c)
