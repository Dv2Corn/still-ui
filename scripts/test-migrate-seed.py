#!/usr/bin/env python3
"""Fill a fresh 3x-ui with inbounds and clients through its own HTTP API.

Test helper for scripts/test-migrate.sh, run inside a throwaway container:

    test-migrate-seed.py <panel base URL> <username> <password> [--check-login] [--only NAME,...]

It creates one inbound per protocol the installed release accepts (VLESS
REALITY, VMess WS, Trojan TLS, Shadowsocks 2022, Hysteria2, AmneziaWG), each
with two clients that have a subscription ID, and prints a JSON summary.
Protocols an old release rejects are skipped and listed under "skipped";
--only limits the set (releases before 2.4 store protocols they cannot run).
With --check-login it only logs in and exits 0 when the panel accepts the
credentials (and 1 when it does not).
"""
import base64
import http.cookiejar
import json
import os
import secrets
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid

BASE, USER, PASSWORD = sys.argv[1].rstrip("/"), sys.argv[2], sys.argv[3]
CHECK_LOGIN = "--check-login" in sys.argv[4:]
ONLY = set()
if "--only" in sys.argv[4:]:
    ONLY = set(sys.argv[sys.argv.index("--only") + 1].split(","))
CSRF = ""
CERT_DIR = "/root/cert/migrate-test"

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def call(path, data=None, form=False):
    url = BASE + path
    headers = {"Accept": "application/json"}
    if CSRF and data is not None:
        headers["X-CSRF-Token"] = CSRF
    body = None
    if data is not None:
        if form:
            body = urllib.parse.urlencode(data).encode()
            headers["Content-Type"] = "application/x-www-form-urlencoded"
        else:
            body = json.dumps(data).encode()
            headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=body, headers=headers, method="POST" if data is not None else "GET")
    with opener.open(req, timeout=30) as resp:
        raw = resp.read().decode()
    try:
        return json.loads(raw)
    except ValueError:
        return {"success": False, "msg": raw[:200]}


def x25519():
    """A REALITY key pair (raw X25519, unpadded base64url, as Xray prints it)."""
    priv, pub = wg_keypair()
    conv = lambda k: base64.urlsafe_b64encode(base64.b64decode(k)).decode().rstrip("=")  # noqa: E731
    return conv(priv), conv(pub)


def wg_keypair():
    """A WireGuard key pair: X25519 private key and its public key, base64."""
    der = subprocess.run(["openssl", "genpkey", "-algorithm", "X25519", "-outform", "DER"],
                         capture_output=True, check=True).stdout
    pub_der = subprocess.run(["openssl", "pkey", "-inform", "DER", "-pubout", "-outform", "DER"],
                             input=der, capture_output=True, check=True).stdout
    return base64.b64encode(der[-32:]).decode(), base64.b64encode(pub_der[-32:]).decode()


def self_signed():
    os.makedirs(CERT_DIR, exist_ok=True)
    crt, key = CERT_DIR + "/fullchain.pem", CERT_DIR + "/privkey.pem"
    if not os.path.exists(crt):
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1",
                        "-nodes", "-days", "30", "-subj", "/CN=migrate.test", "-keyout", key, "-out", crt],
                       capture_output=True, check=True)
    return crt, key


def client(i, tag, **extra):
    c = {"email": f"{tag}-{i}", "enable": True, "subId": f"sub{tag}{i}{secrets.token_hex(3)}",
         "limitIp": 0, "totalGB": 0, "expiryTime": 0, "tgId": 0, "reset": 0, "comment": ""}
    c.update(extra)
    return c


def sniffing():
    return json.dumps({"enabled": False, "destOverride": ["http", "tls", "quic"]})


def inbounds():
    priv, pub = x25519()
    crt, key = self_signed()
    tls = {"serverName": "migrate.test", "certificates": [{"certificateFile": crt, "keyFile": key}],
           "alpn": ["h2", "http/1.1"], "settings": {"fingerprint": "chrome"}}
    yield "vless-reality", {
        "port": 24431, "protocol": "vless",
        "settings": {"clients": [client(i, "vl", id=str(uuid.uuid4()), flow="xtls-rprx-vision") for i in (1, 2)],
                     "decryption": "none", "fallbacks": []},
        "streamSettings": {"network": "tcp", "security": "reality", "tcpSettings": {"header": {"type": "none"}},
                           "realitySettings": {"show": False, "dest": "www.microsoft.com:443", "target": "www.microsoft.com:443",
                                               "xver": 0, "serverNames": ["www.microsoft.com"], "privateKey": priv,
                                               "shortIds": ["6ba85179e30d4fc2"],
                                               "settings": {"publicKey": pub, "fingerprint": "chrome", "serverName": "", "spiderX": "/"}}},
    }
    yield "vmess-ws", {
        "port": 24432, "protocol": "vmess",
        "settings": {"clients": [client(i, "vm", id=str(uuid.uuid4()), security="auto") for i in (1, 2)]},
        "streamSettings": {"network": "ws", "security": "none", "wsSettings": {"path": "/vm", "headers": {}}},
    }
    yield "trojan-tls", {
        "port": 24433, "protocol": "trojan",
        "settings": {"clients": [client(i, "tr", password=secrets.token_hex(8)) for i in (1, 2)], "fallbacks": []},
        "streamSettings": {"network": "tcp", "security": "tls", "tlsSettings": tls, "tcpSettings": {"header": {"type": "none"}}},
    }
    yield "shadowsocks-2022", {
        "port": 24434, "protocol": "shadowsocks",
        "settings": {"method": "2022-blake3-aes-256-gcm", "password": base64.b64encode(os.urandom(32)).decode(),
                     "network": "tcp,udp",
                     "clients": [client(i, "ss", method="", password=base64.b64encode(os.urandom(32)).decode()) for i in (1, 2)]},
        "streamSettings": {"network": "tcp", "security": "none", "tcpSettings": {"header": {"type": "none"}}},
    }
    yield "hysteria2", {
        "port": 24435, "protocol": "hysteria",
        "settings": {"version": 2, "clients": [client(i, "hy", auth=secrets.token_hex(8)) for i in (1, 2)]},
        "streamSettings": {"network": "hysteria", "security": "tls", "tlsSettings": dict(tls, alpn=["h3"]),
                           "hysteriaSettings": {"version": 2, "udpIdleTimeout": 60}},
    }
    # Only on request (--only post-migration): the inbound a test adds after
    # the migration, which a rollback must not carry back to 3x-ui.
    yield "post-migration", {
        "port": 24440, "protocol": "vmess",
        "settings": {"clients": [client(1, "pm", id=str(uuid.uuid4()), security="auto")]},
        "streamSettings": {"network": "tcp", "security": "none", "tcpSettings": {"header": {"type": "none"}}},
    }
    # Only on request (--only mixed): a SOCKS/HTTP inbound without auth on
    # 127.0.0.1, which deploy/test/installer-e2e.sh downloads through while
    # the panel restarts.
    yield "mixed", {
        "port": 24441, "protocol": "mixed",
        "settings": {"auth": "noauth", "accounts": [], "udp": False, "ip": "127.0.0.1"},
        "streamSettings": {},
    }
    server_priv, server_pub = wg_keypair()
    awg_clients = []
    for i in (1, 2):
        cpriv, cpub = wg_keypair()
        awg_clients.append(client(i, "awg", privateKey=cpriv, publicKey=cpub, allowedIPs=[f"10.66.0.{i + 1}/32"], keepAlive=25))
    yield "amneziawg", {
        "port": 24436, "protocol": "amneziawg",
        "settings": {"server": {"privateKey": server_priv, "publicKey": server_pub, "mtu": 1420, "primaryDns": "1.1.1.1",
                                "subnetIp": "10.66.0.0", "subnetCidr": 24, "secondaryDns": ""},
                     "clients": awg_clients},
        "streamSettings": {},
    }


def login():
    """Session login; 3.x also wants the CSRF token of the session."""
    global CSRF
    try:
        CSRF = call("/csrf-token").get("obj") or ""
    except Exception:  # noqa: BLE001 - 2.x has no such endpoint
        CSRF = ""
    if not isinstance(CSRF, str):
        CSRF = ""
    try:
        res = call("/login", {"username": USER, "password": PASSWORD}, form=True)
    except Exception as exc:  # noqa: BLE001
        res = {"success": False, "msg": str(exc)}
    if res.get("success"):
        # The session changes on login; fetch the token that belongs to it.
        try:
            tok = call("/csrf-token").get("obj")
            CSRF = tok if isinstance(tok, str) else CSRF
        except Exception:  # noqa: BLE001
            pass
    return bool(res.get("success")), res


def main():
    ok, res = login()
    if CHECK_LOGIN:
        print("login ok" if ok else "login failed: %s" % res)
        sys.exit(0 if ok else 1)
    if not ok:
        raise SystemExit("login failed: %s" % res)
    created, skipped = [], {}
    listed = call("/panel/api/inbounds/list").get("obj") or []
    taken = {ib.get("port") for ib in listed}
    for name, ib in inbounds():
        if ib["port"] in taken or (ONLY and name not in ONLY) or (name in ("post-migration", "mixed") and name not in ONLY):
            continue
        body = {"up": 0, "down": 0, "total": 0, "remark": name, "enable": True, "expiryTime": 0, "listen": "",
                "port": ib["port"], "protocol": ib["protocol"], "settings": json.dumps(ib["settings"]),
                "streamSettings": json.dumps(ib["streamSettings"]), "sniffing": sniffing()}
        try:
            res = call("/panel/api/inbounds/add", body)
        except Exception as exc:  # noqa: BLE001
            res = {"success": False, "msg": str(exc)}
        if res.get("success"):
            created.append(name)
        else:
            skipped[name] = str(res.get("msg"))[:160]
    print(json.dumps({"created": created, "skipped": skipped}))


if __name__ == "__main__":
    main()
