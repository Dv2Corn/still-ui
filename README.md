**English** | [Русский](README.ru.md) | [فارسی](README.fa.md) | [中文](README.zh.md)

# still-ui

<p>
  <a href="https://www.gnu.org/licenses/gpl-3.0.en.html"><img src="https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true" alt="License: GPL-3.0"></a>
  <a href="https://github.com/MHSanaei/3x-ui"><img src="https://img.shields.io/badge/based%20on-3x--ui%20v3.11.0-555" alt="Based on 3x-ui v3.11.0"></a>
  <a href="https://github.com/Dv2Corn/still-ui/releases/latest"><img src="https://img.shields.io/github/v/release/Dv2Corn/still-ui?label=release" alt="Latest release"></a>
  <a href="https://github.com/Dv2Corn/still-ui/actions/workflows/ci.yml?query=branch%3Amain"><img src="https://github.com/Dv2Corn/still-ui/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/Dv2Corn/still-ui/attestations"><img src="https://img.shields.io/badge/builds-attested-2ea44f" alt="Release builds are attested by GitHub Actions"></a>
</p>

**still-ui** is a fork of [3x-ui](https://github.com/MHSanaei/3x-ui) by MHSanaei — the web panel for [Xray-core](https://github.com/XTLS/Xray-core). It starts from 3x-ui **v3.11.0** and keeps everything that panel can do: the same protocols, the same interface, the same database.

The difference is focus. still-ui fixes the things that hurt when a panel runs for months on a small server with real users on mobile networks: client settings that quietly get lost, handshakes that die on lossy links, and a panel process that slowly eats the server's memory.

The name says what we are after: a server you set up once and then leave alone.

> [!IMPORTANT]
> still-ui is an independent fork. It is not affiliated with or endorsed by the 3x-ui authors. Please report still-ui problems here, not upstream.

## Why switch

| In 3x-ui 3.11.0 | In still-ui |
| --- | --- |
| Since Xray 26.9 the server refuses the `ios` fingerprint and every sing-box client (Hiddify, Karing) ([3x-ui#6596](https://github.com/MHSanaei/3x-ui/issues/6596), [3x-ui#6727](https://github.com/MHSanaei/3x-ui/issues/6727), [Xray-core#7259](https://github.com/XTLS/Xray-core/issues/7259)) | a patched Xray build accepts them again ([details](#what-is-different)) |
| Restarting or updating the panel drops every client | Xray runs as its own service, so a panel restart does not drop Xray clients (AmneziaWG, MTProto and TUIC clients still reconnect; Docker keeps Xray inside the panel) |
| Panel memory grows under AmneziaWG traffic; a runaway panel can push a small server into swap | buffers are reused (403 → 176 MiB under load) and the panel has a memory cap |
| The Telegram bot sends the database unencrypted | backups are encrypted to your key (the private key never touches the server), kept on a schedule, `still-ui restore` puts one back |
| The Telegram bot reports an Xray crash, high CPU and high memory | it also warns when Xray keeps restarting, the disk is full, a certificate is about to expire or a backup failed |

Try it on a running 3x-ui — one command moves it over and checks that panel, inbounds, clients and subscriptions are the same ([what it does](#moving-from-3x-ui)):

```bash
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh) migrate
```

and one command brings 3x-ui back:

```bash
still-ui migrate-rollback
```

## What is different

### Already done

- **Partial client edits no longer break clients.** In 3x-ui v3.11.0, updating a client through the API (`/panel/api/clients/update/:email`) with only some of its fields disabled the client (`enable=false`) and reset `limitIp`, `totalGB`, `tgId`, `comment`, `group`, `flow`, the renewal settings (`reset`, `resetMax`) and `limitHwid`. In still-ui a field you do not send is taken from the stored client. An explicit zero or empty value still clears a field. WireGuard/AmneziaWG keys and peer addresses were already kept in v3.11.0; a test pins that.
- **The Xray core accepts the `ios` fingerprint and sing-box clients again.** Since Xray 26.9.9, an official REALITY server accepts only a ClientHello that carries an X25519MLKEM768 key share. That refuses Xray clients with the `ios`, `edge` and `qq` fingerprints, every sing-box client (Hiddify, Karing) with any fingerprint, and mihomo unless `support-x25519mlkem768` is on. `ios` is the short one (about 517 bytes, one TCP segment), and on mobile networks that drop longer ClientHellos it is the fingerprint that works. still-ui 1.4.0 ships Xray v26.10.31 built from source with a one-hunk REALITY patch: a ClientHello without that key share is accepted again, duplicated key shares are still refused, nothing else changes. The build is reproducible: recipe, patch and sha256 in [scripts/xray-build](scripts/xray-build/README.md). New REALITY inbounds still start with `chrome`, as in 3x-ui. Switching Xray to an official release from the panel (the **Version** button next to Xray on the overview page) brings the old behaviour back; `still-ui update` restores the still-ui build. The dialog marks the still-ui build apart from the official releases and, before installing an official one, shows this warning and asks you to confirm.
- **The installer puts a memory cap on the panel.** The service gets systemd limits: `MemoryMax` is 35% of RAM (at least 256 MiB, at most 1 GiB), `MemoryHigh` is 85% of that, and the panel is not allowed to use swap. A runaway panel is restarted by systemd instead of pushing the whole server into swap and freezing it. If the server has no swap and 2 GiB of RAM or less, the installer offers to create a 2 GiB `/swapfile` for the rest of the system.
- **Xray keeps running when the panel restarts.** Xray runs as its own service `still-xray`: an unprivileged user, only `CAP_NET_BIND_SERVICE` and `CAP_NET_ADMIN` (socket marks, TPROXY), `ProtectSystem=strict`, `Restart=always` and its own memory cap. Restarting, updating or crashing the panel does not drop clients. The panel still adds and removes clients through the Xray API without a restart, and restarts the core only when the inbound structure changes — after checking the config with `xray run -test`; a config that passes the check but does not start is replaced with the last working one. This is the default on every server with systemd; an existing install moves over on its next `still-ui update`, unless its config needs something the service cannot do (see below). Docker images keep Xray inside the panel, as in 3x-ui. The embedded AmneziaWG, MTProto and TUIC still live inside the panel and reconnect when it restarts.
- **Scheduled, encrypted backups and a restore command.** The panel can back up its database on a schedule to `/etc/still-ui/backups`, keep the last N copies, encrypt them to your [age](https://age-encryption.org) or SSH public key (the private key never touches the server) and send the encrypted copy to Telegram. `still-ui restore <file>` checks the backup, stops the panel, swaps the database (keeping the old one) and starts it again.
- **Health alerts in Telegram.** Xray not running, Xray restarting too often, panel or Xray memory near its limit, a full disk, a certificate about to expire, a failed backup. One message when it starts, a reminder while it lasts, one when it is fixed.
- **Renewed certificates are picked up automatically.** Xray rereads the certificate files of TLS and Hysteria inbounds by itself once an hour (or every `ocspStapling` seconds). Once a minute the panel does a handshake (TLS, or QUIC for Hysteria) to see which certificate each inbound really serves; if the core has not picked up a renewed file within that time (or the inbound uses `oneTimeLoading`), or the served certificate expires within an hour, only Xray is restarted: by itself with the `still-xray` service, and with Xray inside the panel (Docker, or STILL_XRAY_UNIT=off) only with `STILL_CERT_RESTART=on` in `/etc/default/still-ui`, because that restart drops every client; without it the tag just says the certificate was renewed. The “Certificates” tag on the overview page shows what each inbound serves and until when, and warns in advance (a week ahead, or a quarter of the lifetime for short-lived IP certificates).
- **AmneziaWG relay buffers are reused.** Every TCP connection through the embedded AmneziaWG allocated two fresh 32 KiB copy buffers; they now come from a pool. The relay alone: about 69 KB → 2.9 KB allocated per connection. The whole panel on a 2 vCPU / 2 GiB test server, 100 short connections per second through AmneziaWG for 5 minutes, 3 runs each, 3x-ui v3.11.0 vs still-ui: panel memory under load 403 → 176 MiB on average, peak 525 → 211 MiB, the same at rest. Method, limits and raw data: [bench/awg-relay-churn](bench/awg-relay-churn/README.md).
- **An Auto group in Clash/Mihomo subscriptions.** With two or more servers, the Clash/Mihomo profile gets an `Auto` group: the app tests every server through the tunnel and moves to a working one by itself (fastest with a 50 ms tolerance, checked every 300 s, or the first that answers). `Auto` is the default choice in `PROXY`; picking a server by hand still works. On for new installs; on a panel that existed before, turn on **Auto group** in the subscription settings.
- **Subscriptions follow the reachability probes.** A link whose endpoint the [probes](#check-reachability-from-russia) report down (two failed checks in a row) can be moved to the end of the subscription or left out, in every format: links, JSON and Clash. A user always keeps at least one link, and without fresh probe results nothing changes. Off by default.
- **Local sites open directly, with one setting.** Pick a country in **Settings → Subscription → Local sites direct** and client apps open that country's sites directly (its domain zones, the curated `geosite` list and `geoip`) and send everything else through the VPN, so banks, government portals and marketplaces that refuse foreign addresses keep working with the VPN on. A short always-VPN list is checked first — Telegram (its address ranges are listed as Russian), YouTube, Instagram, WhatsApp, Discord, ChatGPT, Claude — and you can add your own entries. Works in JSON, Clash/Mihomo and Happ/INCY subscriptions, plus a Shadowrocket rules file on the subscription page; a plain list of links and AmneziaWG configs cannot carry such rules. Off by default. [Details](docs/content/docs/en/config/subscription.mdx)
- **An egress filter for clients.** Clients can be kept from sending mail on port 25 (on by default for new installs), from reaching private networks behind the server and the server's own panel and SSH ports, and from addresses and ports you list. It applies to every client inbound through Xray and, with `nft`, to AmneziaWG kernel-mode clients; a master can hand it to its nodes. [Details](#mobile-networks-in-russia).
- **A monthly traffic limit for each node.** The master counts the traffic of a node's own inbounds per month (from the reset day you pick), sends a Telegram alert at 80% (adjustable) and at the limit, and can turn the node's inbounds off until the next period. An accounting multiplier from 0.1 to 100 scales that node's traffic in client totals, from the moment it is set (earlier traffic keeps its old rate). The node checks each client's quota by its own master's count, so masters with different multipliers can share a node. No limit by default.
- **Adding a node takes one command.** **Nodes → Add with a command** makes a one-time invitation (valid 30 minutes by default, can be revoked); running the command it shows as root on a new server installs still-ui from the release, sets it up as a node and adds it to the master. No API tokens or certificate pins to copy by hand; the node checks the master's key before it changes anything. [Guide](docs/content/docs/en/operations/multi-node.mdx).
- **The device limit tells the person what happened, has a panel default and forgets old devices.** 3x-ui v3.11.0 can cap the devices of a subscription (a client's **HWID Limit**; unlike **IP limit** it needs no Fail2ban and does not trip when a phone moves between Wi-Fi and mobile data), but a device over the limit gets a bare 404, an app that sends no device ID is always refused, there is no panel-wide default and a device holds its slot until an admin removes it. In still-ui a new device over the limit gets a valid answer for its app whose only server is a notice — "Device limit 3 reached. Remove a device in your account or ask the seller." (the text is yours; Happ also shows it as the announcement) — and never an error; devices registered before keep getting the servers. **Settings → Subscription → Device limit** adds one default limit for every client without its own (off until you set it; a subscription that already has more devices keeps the most recently seen ones), a choice for apps that send no device ID — all of them share one slot of the subscription (the default on a new panel), they are refused with a notice, or they are served and not counted (what an upgraded panel without limits keeps; with it the limit can be bypassed by an app that leaves the ID out, and the panel warns about that) — and expiry: a device that has not fetched the subscription for 30 days (adjustable; off on an upgraded panel until you set it) frees its slot. The subscription page shows the person "2 / 3" devices and nothing about them; the admin sees and removes devices in the client's card, the API lists and deletes them (`/panel/api/clients/hwids/:email`). The panel stores a hash of the device ID, never the ID. The limit is on fetching the subscription: servers already imported into an app keep working, and links copied by hand are not counted.
- **Moving from Marzban or an old x-ui takes one command.** `still-ui import marzban /var/lib/marzban` brings inbounds, users and hosts over with the same UUIDs and passwords, the same REALITY key and short IDs, ports, traffic used, limits, expiry dates and states, so the links users already have keep connecting, and the subscription links Marzban issued keep working. `-dry-run` shows what would be imported and what cannot be; a real import takes a backup first and writes all or nothing. The same is in **Settings → Import**. See [Moving from Marzban or x-ui](#moving-from-marzban-or-x-ui).
- **Panel memory over several days: checked.** 72 hours under mixed load (200 users; a client change every 5 s; UI and API calls; subscriptions; VLESS, VMess and AmneziaWG traffic), still-ui v1.6.0 next to 3x-ui v3.11.0 with the same data and the same load: no growth in either (+0.1 MB/h over the last 24 h, goroutines and file descriptors flat, no restarts). Many *slow* AmneziaWG clients can still hold kernel socket buffers (about 12 MB per slow stream) that count against the panel; the memory cap the installer sets for the service keeps that in check (with Docker, set `mem_limit`).
- **A connection log in the panel.** The **Connections** page shows who connected, when, through which inbound and where to, with filters by client, inbound, time, destination and source address, and a CSV export; the icon in a client's row opens that client's connections. An abuse complaint ("who reached this host at that time") is answered without shell access. Off by default. You choose how long it is kept (30 days by default), a size limit, and what is stored: the full destination, only its registered domain or nothing; the client's full address, only its network or nothing. Old days delete themselves. [How it works](#connection-log).
- **A REALITY target picker that shows every check.** **Find Targets** on a REALITY inbound tests each candidate from your server and marks what passed: reachable, TLS 1.3, X25519 (and whether X25519MLKEM768 is accepted too), HTTP/2, a trusted certificate for the name, more than 14 days left on it, latency. Candidates are a short list you can edit, anything you type and, if you tick the box, the other addresses in your server's own /24. **Use** fills the target and the SNI. No list in the panel is a recommendation: which site to imitate is your choice. [Details](#mobile-networks-in-russia)
- **Hysteria2 port hopping that the panel sets up itself.** In 3x-ui the hop range is only told to clients, and the redirect on the server is yours to write. **UDP Hop → Redirect on this server** loads an nftables rule that sends the range to the inbound's port, restores it after a reboot and removes it with the inbound. Where it cannot work (no `nft`, a container without `NET_ADMIN`) the panel refuses the option instead of handing clients dead ports. Off by default. [Details](#mobile-networks-in-russia)
- **AmneziaWG: a first packet that looks like QUIC or DNS, and MTU 1280 in one click.** Two buttons build an `I1` signature packet shaped like a QUIC Initial or a DNS query; the random parts differ on every handshake. Each preset completes a handshake between a real amneziawg-go client and server in the test suite. The buttons change only `I1`–`I5`, so clients you already have keep working. **Set 1280** next to the MTU: on Russian mobile networks AmneziaWG measured 4–5 times faster at 1280 than at the default. [Details](#mobile-networks-in-russia)
- **A preset for VLESS over XHTTP with REALITY.** **Add Inbound → Preset → Mobile networks (Russia): VLESS XHTTP REALITY** fills VLESS on TCP 443 with the XHTTP transport and REALITY: a fresh key pair and short IDs, the `ios` fingerprint, a random path, mode `auto` and 100–1000 bytes of padding; the target is picked on the **Security** tab, as with the other REALITY preset. Clients get no flow: XTLS Vision does not work over XHTTP. Share links, the JSON subscription and the Clash/Mihomo profile carry the transport; in the test suite an Xray client with each of them and mihomo 1.19.22 and 1.19.33 fetch a page through still-ui's Xray. XHTTP does not split a download: 50 MB went as one HTTP response over one TCP connection, so it is no way past a cut after the first kilobytes of a connection. [Details](#mobile-networks-in-russia)

### In progress

- Nothing at the moment.

The full plan (in Russian) is in [docs/ROADMAP.md](docs/ROADMAP.md).

## Install

Run as root:

```bash
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh)
```

To install a specific release, pass its tag (tags look like `vX.Y.Z-still.N`):

```bash
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh) v1.4.0
```

These commands run the installer of the latest release: since 1.5.1 every release publishes `install.sh` itself and lists it in its `SHA256SUMS`, so nothing is run from a branch. To check the installer before running it:

```bash
curl -fsSLO https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh
curl -fsSLO https://github.com/Dv2Corn/still-ui/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing && bash install.sh
```

The installer refuses an archive whose `.sha256` is missing or does not match, and installs the `still-ui` menu from that checked archive.

To install from a release archive you already downloaded:

```bash
STILL_UI_ARCHIVE=/path/still-ui-linux-amd64.tar.gz bash install.sh
```

The installer generates a random username, password and panel path and prints them at the end — save them. After that, run `still-ui` to open the management menu: start and stop the service, show or reset the login, change the port, manage certificates and so on. The subcommands are the same as in 3x-ui's `x-ui` script (`still-ui start`, `stop`, `restart`, `status`, `log`, `update`, `legacy`, `uninstall` and others).

Release builds exist for **Linux amd64 and arm64** only. The current release is still-ui **1.7.1**, based on 3x-ui 3.11.0 and the fixes merged after it, with **Xray v26.10.31** in still-ui's own build ([what differs](scripts/xray-build/README.md)).

### Where things live

| What | Where |
| --- | --- |
| Service | `still-ui` (`systemctl status still-ui`) |
| Program | `/usr/local/still-ui` |
| Database | `/etc/still-ui/x-ui.db` (the file name is kept from 3x-ui) |
| Logs | `/var/log/still-ui` |
| Settings | `/etc/default/still-ui` |
| Xray | service `still-xray`, config `/etc/still-xray`, logs `/var/log/still-xray` |
| Backups | `/etc/still-ui/backups` (`XUI_BACKUP_FOLDER` in the settings file moves them) |
| Connection log | `/etc/still-ui/connlog` (one file per day; not part of the backups) |

### Memory cap

Check the current cap:

```bash
systemctl show still-ui -p MemoryMax
```

To set your own value, add a line to `/etc/default/still-ui` — for example `STILL_UI_MEMORY_MAX=800M`, or `STILL_UI_MEMORY_MAX=off` to remove the cap.

### Xray keeps running during panel restarts

Xray runs as the systemd service `still-xray`, apart from the panel. `systemctl restart still-ui`, a panel update (from the command line or the update button) and a panel crash leave it alone, so clients stay connected. Tested in a throwaway Debian 12 container: a 20 MB download through a SOCKS inbound at 400 KB/s, with `systemctl restart still-ui`, `kill -9` of the panel and a full `still-ui update` in the middle — each time the file arrived complete and the Xray process was the same.

When does Xray restart? When the inbound structure changes (the panel says so), when you press **Restart Xray**, after `still-ui restore`, and once when an install moves to the service. A new Xray from an update runs only after you restart Xray; the overview page says so.

Check it: `systemctl status still-xray`. The settings file `/etc/default/still-ui` has `XUI_XRAY_UNIT=still-xray` while the service is in use.

On update, `x-ui xray-unit-check` looks at the current config first. Xray stays inside the panel, and the update prints why, when an inbound
- listens on a unix socket outside `/run/still-xray/` and `/dev/shm/` (the service has its own `/tmp` and sees the rest read-only; use e.g. `/dev/shm/xray.sock,0666`),
- is a TUN inbound: it needs `/dev/net/tun`, which the service's sandbox hides; a drop-in of your own in `/etc/systemd/system/still-xray.service.d/` with `PrivateDevices=no` allows it, then run the update again.

TPROXY and socket marks (policy routing, WARP) work in the service: it has `CAP_NET_ADMIN`. The panel refuses a change the running service cannot do and says why, for example a socket mark after a local drop-in took the capability away; a core still running from before the update that gave the service the capability is restarted once for a change that needs it.

Settings in `/etc/default/still-ui`: `STILL_XRAY_UNIT=off` keeps Xray inside the panel (as in 3x-ui), `STILL_XRAY_UNIT=on` forces the service, unset means automatic. `STILL_XRAY_MEMORY_MAX` caps the core (half the RAM by default, 256M to 4G). After a change run `still-ui update`. Docker images have no systemd and always keep Xray inside the panel.

### Backups and restore

**Settings → Backups** in the panel:

- **Scheduled backups** on, and **Schedule** (daily by default). The panel takes a consistent snapshot (SQLite backup API; `pg_dump` with PostgreSQL), checks it reads back, and writes `still-ui-<host>-<date>-<time>.db` (`.pgdump`) to `/etc/still-ui/backups` (0600, folder 0700). Restart the panel after changing the schedule.
- **Copies to keep** — older ones are deleted after each new backup; other files in the folder are never touched.
- **Encryption keys** — age public keys (`age1…`, from `age-keygen` on your own computer) or SSH public keys (`ssh-ed25519 …`), one per line. With a key set, backups are written as `….db.age` and nothing unencrypted is stored. The private key stays with you. The daily Telegram backup (**Telegram Bot → Notifications → Database Backup**) is encrypted to the same keys, the Xray config too.
- **Send to Telegram** — each encrypted backup also goes to the Telegram admins (up to 50 MB). Unencrypted backups are never sent this way.
- **Back up now** and the list of backups on the server. From the command line: `still-ui backup`.

Restore:

```bash
still-ui restore /etc/still-ui/backups/still-ui-<host>-<date>-<time>.db.age
```

For an encrypted backup the script asks you to paste the private key (`AGE-SECRET-KEY-1…`); it goes to the panel binary through a pipe and is not stored. You can also pass a key file as the second argument (an age identity file or an unencrypted OpenSSH private key) — copy it to the server only for the restore. The backup is decrypted and checked first (`PRAGMA integrity_check`, `pg_restore --list`); a wrong key or a damaged file changes nothing. Then the panel stops, the database is replaced, the old one is kept as `/etc/still-ui/x-ui.db.before-restore-<time>`, Xray restarts once to load the restored inbounds, and the panel starts again. On PostgreSQL the old database is kept as a dump beside it.

You can also decrypt elsewhere: `age -d -i key.txt backup.db.age > x-ui.db`.

### Alerts

**Settings → Telegram → Notifications → Server health** (the bot must be on):

| Alert | Fires when | Default |
| --- | --- | --- |
| Xray is not running | Xray is down for two checks in a row (a minute), and you did not stop it | on |
| Xray restarts | Xray crashed or was restarted by systemd N times in M minutes | 3 in 10 min |
| Panel or Xray memory | the panel or Xray uses X% of its systemd `MemoryMax` (without file cache), or of the RAM when there is no cap; three checks in a row; clears after three checks 15 points below | 90% |
| Disk full | the disk with `/`, the database or the backups is X% full; clears after three checks 5 points below | 90% |
| Certificate expires | the panel's or the subscription's certificate file, or the certificate a TLS/Hysteria inbound actually serves, expires within N days — at most a quarter of its lifetime, so a 6-day IP certificate is reported only in its last day and a half | 7 days |
| Backup failed | a scheduled backup (or its Telegram upload) failed | on |

Checks run every minute. An alert is sent once when the problem starts, again every **Remind every** hours while it lasts (6 by default, 0 = never), and once more when it is fixed. The state lives in memory: after a panel restart a problem that is still there is reported once again.

### Connection log

**Settings → Connection log** in the panel, then the **Connections** page:

- **Record connections** — off by default. While it is on, the panel reads Xray's access log every five seconds. If that log is set to `none`, the panel turns it on for as long as the switch is on (`access-connlog.log` in Xray's log folder), which restarts Xray once; an access log you set yourself is used as it is, with no restart. Turning the switch off puts the Xray config back and empties that file.
- **Keep for (days)** (30) and **Size limit (MB)** (2048). Each day is one file in `/etc/still-ui/connlog`, outside the panel database and its backups. Days past the retention are deleted; when the folder grows past the limit, the oldest day goes first, and if a single day is larger than the limit, its oldest connections go. A million connections take about 75 MB and about half a minute of one CPU core to store.
- **Destination**: the full host name or IP address, only the registered domain (`maps.example.co.uk` becomes `example.co.uk`; an IP address stays as it is), or nothing. **Client address**: the full address and port, only the network (/24 for IPv4, /48 for IPv6, no port), or nothing. A change applies to new connections; what is already stored stays as it is until it expires.
- **Connections** — filter by client (exact name), inbound, time range, part of the destination and part of the source address. Newest first, 50 per page. **Export CSV** saves up to 100 000 rows of the current filter, times in UTC.
- Only an admin can read it: the page and its API (`/panel/api/connlog/list` and `/export`) need a panel login or an admin API token; monitor and node tokens are refused. Every query and export is written to the panel log with the user, their address and the filter.

Limits. The log holds what passes through Xray: AmneziaWG and TUIC connections are logged with their client and destination, but with a local source address, because the panel hands them to Xray on the same machine; MTProto connections are not attributed to a client. With `maskAddress` set in Xray's log settings, Xray hides part of the client address before the panel sees it. Each node keeps its own log, which you read in that node's panel; the master does not collect it. When the panel empties a large access log, it stores the lines it has not read yet first. The storage setting below covers what the panel keeps, not Xray's access log itself: that file holds whole lines until the panel empties it, once a day or when it passes 64 MiB. The log is as trustworthy as Xray's access log, and no more: Xray writes a refused request's text into it as it came, line breaks included, so someone who can send a request to an inbound can add a line of their choice, with any client name, address and destination. The panel leaves out such a line when its date is ahead of the clock, and says so in the panel log; it cannot tell one dated now from Xray's own. Treat a row as a lead, not as proof.

## Update, roll back, remove

Update to the latest release — from the command line or with the update button in the panel:

```bash
still-ui update
```

Both download `update.sh` of the release they install (`releases/download/<tag>/update.sh`) together with that release's `SHA256SUMS`, and run it only if its sha256 is listed there; nothing is run from a branch. still-ui has no dev channel: every update installs a release. The first update from 1.5.0 or older still runs the old updater (it takes `update.sh` from `main`, which only changes through reviewed pull requests); from the next release on the release's own file is used.

Roll back to an earlier release — `still-ui legacy` asks for the version; or run the installer with that tag. The database is kept.

```bash
still-ui legacy
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh) v<tag>
```

Rolling back to a release without the `still-xray` service (for example `v3.11.0-still.1`): `still-ui legacy` stops the service before installing; if you run an older installer by hand, run `systemctl disable --now still-xray` first.

Back up `/etc/still-ui` before updating or rolling back: an older version may not understand a database written by a newer one.

Remove the panel:

```bash
still-ui uninstall
```

This removes the program, both services, the `still-xray` user and the menu, and keeps the data: the database, backups and `install-result.env` in `/etc/still-ui` and the env file (`/etc/default/still-ui`). Installing still-ui again picks them up, with the same login, inbounds and clients. The menu asks "Also delete all data?" (default No); without a terminal the data is always kept. For scripts: `still-ui uninstall --keep-data` keeps it, `still-ui uninstall --purge` deletes it too (and offers to purge a local PostgreSQL). Unlike 3x-ui, nothing is deleted unless you ask.

## Mobile networks in Russia

Russian mobile operators inspect traffic (DPI), and in "whitelist" mode they cut most foreign TCP connections after the first 12–16 KB. still-ui has several things for this.

**Presets for new inbounds.** In **Add Inbound**, pick a **Preset**:

- **Mobile networks (Russia): VLESS REALITY** — VLESS on TCP 443 with REALITY, a fresh key pair and short IDs, and the `ios` fingerprint in the links and subscriptions of this inbound only (other new inbounds still start with `chrome`). The `ios` ClientHello is about 517 bytes and fits in one TCP segment; longer ones lose their second segment on some networks. still-ui's Xray accepts `ios`; official Xray 26.9.9 and newer refuses it. Then pick a target on the **Security** tab. **Find Targets** checks candidate sites from your server; prefer a site that is popular and not blocked in your clients' country over one well-known foreign site. Give clients the `xtls-rprx-vision` flow.
- **Mobile networks (Russia): VLESS XHTTP REALITY** — the same VLESS REALITY on TCP 443 with the `ios` fingerprint, over the XHTTP transport: inside the TLS connection the traffic is HTTP/2 requests to a random path, with 100–1000 bytes of padding. Mode `auto`, which under REALITY in Xray 26.10.31 is `stream-one`: every connection a client app opens becomes one HTTP/2 request. Leave the clients' flow empty: XTLS Vision does not work over XHTTP (an Xray client with it does not connect). Clients need XHTTP support: apps built on Xray-core, or mihomo 1.19.22 and newer (the Clash/Mihomo profile carries `xhttp-opts`). XHTTP does not split a download into short requests: through still-ui's Xray a 50 MB file went as one HTTP response over one TCP connection in every mode (`packet-up` splits only uploads, into POSTs of up to 1 MB). So this preset does not get past a cut after the first 12–16 KB of a connection.
- **Mobile networks (Russia): Hysteria2 (UDP)** — Hysteria2 on UDP 443 with the `h3` ALPN and no Salamander obfuscation, so that most clients can connect. UDP often keeps working in whitelist mode when TCP is cut. Set the certificate on the **Security** tab.

**Find a REALITY target.** **Find Targets** (next to the target on the **Security** tab) checks candidates from the server itself and shows each check as passed or failed: the name resolves and TCP connects, TLS 1.3, X25519 key exchange (an **ML-KEM** tag when the site also accepts X25519MLKEM768), HTTP/2, a certificate that is trusted for the name, and latency. A certificate with less than 14 days left gets a warning and such targets are listed after the others. The candidates are the **Reality scan candidates** list in **Settings** (ten large sites by default; none of them is a recommendation), any domain, address or network you type, and, only if you tick **Also check my server's neighbours**, port 443 of the other addresses in the /24 around each public IPv4 address of the server, with the names taken from their certificates. The neighbour scan opens one connection per address, about 25 a second, one scan at a time; some hosting providers treat that as a port scan. A scan stops starting new checks after 17 seconds and says how many targets it did not reach. Private, loopback, link-local and carrier-NAT addresses are never probed in these scans, and a name is checked after it is resolved, on the address the panel then connects to. **Use** fills the target and the SNI.

**Hysteria2 port hopping.** Clients that hop send to a different UDP port every few seconds, which keeps a connection alive where a single long UDP flow gets throttled. On a Hysteria inbound, on the **Stream** tab turn on **QUIC Params** and **UDP Hop**, set the range (for example `20000-30000`) and turn on **Redirect on this server**. Then:

- The panel loads the nftables table `inet still_port_hop`: UDP that arrives for this server on the range is redirected to the inbound's port, for IPv4 and IPv6. Forwarded traffic is not touched, and a UDP port of another inbound is never redirected. The table is loaded on start, restored within a minute if something flushes it, and removed within 10 seconds after the option is turned off or the inbound is disabled or deleted; `still-ui uninstall` removes it too.
- The range must lie in 1024–65535, must not contain the UDP port of another inbound or a port forwarded to an AmneziaWG client, or overlap another inbound's range, and the inbound must listen on all addresses. The panel cannot see UDP services it does not manage (a separate WireGuard or a UDP port published by Docker, for example): keep them out of the range. Prefer a range below 32768: Linux takes the ports of the server's own outgoing connections from 32768–60999, and a late reply to one of them would be redirected too. Open the range in your hosting provider's firewall; a firewall on the server itself needs only the inbound's own port.
- Without `nft`, outside Linux, or in a container without the `NET_ADMIN` capability the option is refused with the reason, so clients are never given ports that lead nowhere. In Docker the hop ports must also reach the container (host networking or published ports).
- Clients get the range in the form they read: `mport` in `hysteria2://` links, `ports` and `hop-interval` in Clash/Mihomo, a `udphop` mask in the Xray JSON subscription. The panel has no sing-box format.
- With nodes, the node's own panel installs the redirect. A node that has not been updated to this version ignores the switch and only advertises the range, as before.

The switch is off for every inbound that exists today; a range entered earlier is still only advertised until you turn it on.

**AmneziaWG on mobile networks.** In an AmneziaWG inbound, next to **Regenerate**:

- **QUIC-like I1** and **DNS-like I1** replace the signature packets: `I1` becomes a packet that starts like a QUIC v1 connection (1200–1232 bytes) or a DNS query for a well-known name, and `I2`–`I5` are cleared. The connection id, the query id and the payload are random on every handshake. The receiving side ignores these packets, so clients that already have a config keep connecting, and new configs carry the new packet. It is a disguise of the first packet only, not a real QUIC or DNS exchange.
- **Set 1280** under **MTU** puts 1280 into the inbound; every client config the panel generates then carries `MTU = 1280`. On Russian mobile networks AmneziaWG measured 4–5 times faster at 1280 than at the default.

**Block torrents for clients.** **Xray Configs → Routing → Basic Routing → Block torrents for clients.** It is off by default, and clients get no notice. When it is on:

- Every Xray config sends BitTorrent (as Xray's sniffer sees it) to a blackhole before any other rule. Inbounds with sniffing off get content sniffing without destination override, because Xray only recognises BitTorrent when it sniffs.
- On Linux with `nft`, the panel loads the nftables table `inet still_torrent_block`. It looks only at what the server sends out: connections the server itself opens, and traffic forwarded from AmneziaWG kernel-mode interfaces (`xui-awg*`), which never passes through Xray. Outgoing BitTorrent handshakes, DHT messages, UDP tracker connects and uTP SYNs are dropped; an AmneziaWG kernel-mode client that sent one keeps only ports 53/80/443/853 for 30 minutes (a client can only cut itself). A peer endpoint (address and port) is cut for 30 minutes only after that peer itself answers with a BitTorrent handshake or a DHT response, so a client cannot get some other site blocked by sending it a fake handshake. One risk remains: a TCP service that echoes what it receives back at the start of its reply can be made to send a 68-byte BitTorrent handshake, and then its own address and port are cut for 30 minutes. Answers from ports 53/80/443/853, from the panel, subscription, inbound, node and SSH ports, and from the server's own addresses are never learned, and each remote address can teach at most 30 endpoints a minute. The server's replies on connections that clients open to it are never inspected. The panel loads the table on start and again if it disappears; turning the option off removes it. Without `nft` (or not on Linux) only the Xray part works, and the page says so: install the `nftables` package. The Docker image includes it.

BitTorrent encrypted inside a client's own extra tunnel cannot be seen. Traffic that Xray passes on to another proxy is covered by the Xray rule only. With nodes, turn the option on in each node's own panel.

**Egress filter for clients.** **Xray Configs → Routing → Basic Routing → Egress filter for clients.** It keeps clients from using the server for mail spam, from reaching networks behind it and from reaching its management ports; hosters suspend servers over complaints about exactly these. Tick what to refuse and press **Apply**:

- **Mail to mail servers (port 25)** — on by default on new installs, off on existing ones. Spam from VPN clients goes out on port 25, while mail apps send on 465/587.
- **Mail submission (ports 465 and 587)**.
- **Private and local networks**: 10/8, 172.16/12, 192.168/16, 100.64/10 (carrier-grade NAT), 169.254/16, loopback, multicast, reserved, fc00::/7, fe80::/10. The stock template already sends `geoip:private` to a blackhole; the panel sees that rule and adds no second one.
- **This server's panel and SSH ports**, read from the panel settings and `sshd_config`, and the server's loopback services. Besides the interface addresses, the block covers IP literals in the panel's listen and domain settings and, on a node, the IP address its master reaches it at; names are never resolved, so a domain behind a CDN or a shared host blocks nothing. A public address on no interface (behind NAT or a floating IP) that is not listed there goes into **Custom addresses and ports**. The subscription port stays open, because client apps refresh their subscription through the tunnel they are connected to; a port that also serves an inbound stays open too.
- **Custom addresses and ports**: an address or CIDR is refused on every port, a port or range (`6667`, `1000-2000`) on every address. Port 53 cannot be listed: DNS always works.

Refused TCP connections are closed at once instead of hanging. Every generated Xray config gets these rules ahead of all others, for client inbounds only (including AmneziaWG, TUIC and MTProto relayed through Xray), plus block final rules in each freedom outbound for the server's own ports and for the private, loopback and listed addresses (every port but 53), since a client that names such an address by a domain is only resolved there. On Linux with `nft`, the table `inet still_egress_filter` refuses the same for AmneziaWG kernel-mode clients (`xui-awg*`): it judges only connections a tunnel client opens, never the server's own traffic, SSH sessions, DNS or replies, and traffic between two clients of the same tunnel. It is loaded on start and reloaded if it disappears.

With nodes, choose **All nodes** or **Selected nodes**: the panel hands the filter to those nodes and keeps retrying the ones that are offline; nodes taken out of the list keep their last filter as their own. A node shows that its filter was set by the central panel; saving it on the node makes it the node's own until the central panel sends it again.

## Verify a release

Release archives are built by GitHub Actions from the tagged commit ([release workflow](.github/workflows/release.yml)), not on a developer's machine, and every archive comes with a signed [build provenance attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations). Releases up to and including 1.4.0 were built by hand with `scripts/still-release.sh` and have only the `.sha256` files.

| File | What it is |
| --- | --- |
| `still-ui-linux-<arch>.tar.gz` | the archive `install.sh` and `still-ui update` download |
| `still-ui-linux-<arch>.tar.gz.sha256` | its checksum; the installer checks it |
| `still-ui-linux-<arch>.tar.gz.spdx.json` | SBOM (SPDX): every file in the archive with its sha256, and the Go modules inside `x-ui`, `xray` and `mtg` |
| `still-probe-linux-<arch>.tar.gz` (+ `.sha256`) | the reachability probe agent that `scripts/still-probe-install.sh` downloads; attested like the panel archives |
| `install.sh`, `update.sh`, `x-ui.sh`, `migrate.sh` | the installer, the updater, the `still-ui` menu and the 3x-ui migration of this release (the same files are inside the archive); the panel's update button and `still-ui update` run `update.sh` only when its sum is in `SHA256SUMS` |
| `SHA256SUMS` | checksums of the archives, SBOMs, probe archives and scripts |

Download and check the checksums (replace `v1.7.1` with the release you install):

```bash
gh release download v1.7.1 -R Dv2Corn/still-ui -p 'still-ui-linux-amd64.tar.gz*' -p SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

Check that the archive was built by this repository's release workflow from that tag (needs the [GitHub CLI](https://cli.github.com/)):

```bash
gh attestation verify still-ui-linux-amd64.tar.gz -R Dv2Corn/still-ui \
  --signer-workflow Dv2Corn/still-ui/.github/workflows/release.yml --source-ref refs/tags/v1.7.1
```

The SBOM is attested too:

```bash
gh attestation verify still-ui-linux-amd64.tar.gz -R Dv2Corn/still-ui --predicate-type https://spdx.dev/Document/v2.3
```

The Xray core inside the archive can be rebuilt bit for bit. On a Linux host with git and docker:

```bash
tar -xzf still-ui-linux-amd64.tar.gz still-ui/bin/xray-linux-amd64
sha256sum still-ui/bin/xray-linux-amd64
git clone https://github.com/Dv2Corn/still-ui && cd still-ui && git checkout v1.7.1
scripts/xray-build/build.sh /tmp/xray-out amd64 arm64
```

`build.sh` prints the sums of the binaries it built and fails if they differ from the ones pinned in it; the sum of `xray-linux-amd64` from the archive must be the same. The [Xray reproducible build](.github/workflows/xray-reproducible.yml) workflow does this on every change to the recipe and once a week, on an x86_64 and an arm64 runner. What the patch changes: [scripts/xray-build](scripts/xray-build/README.md).

Everything else that builds or goes into the archive is pinned in [`scripts/still-release.sh`](scripts/still-release.sh), and the build fails on any other sum: the `node` and `golang` build images by digest, the Bootlin musl toolchain that links the panel binary by file name and sha256, and `mtg` by release and the sha256 of each archive. The geo files are data that has to stay current, so they are the latest releases of their projects at build time; the build log prints the repository, release tag and sha256 of each, and the SBOM lists them with their sha256. The archive as a whole is still not bit-for-bit reproducible (archive timestamps, the geo files), so the provenance attestation is what ties it to the commit and the workflow run.

## Moving from 3x-ui

One command moves a running 3x-ui to still-ui and keeps everything clients and you rely on:

```bash
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh) migrate
```

What it does:

1. Finds 3x-ui (`/usr/local/x-ui`, `x-ui.service`, the SQLite database or the PostgreSQL one named in `/etc/default/x-ui`) and refuses what it cannot move safely: other x-ui forks, 3x-ui older than 2.0.0 (update it first with `x-ui update`), a 3x-ui newer than the release still-ui is based on, hosts without systemd, Docker installs, hosts that already run still-ui.
2. While 3x-ui still runs, records what has to survive: inbounds, clients, the panel login, the panel and subscription addresses, and the subscriptions of up to five clients as 3x-ui serves them.
3. Backs up everything to `/root/still-ui-migrate-<UTC time>/` (readable by root only) before changing anything: a consistent copy of the database (or a `pg_dump`), `/usr/local/x-ui`, the unit and its drop-ins, `/usr/bin/x-ui`, the env file, and a manifest with checksums.
4. Installs the packages still-ui needs while 3x-ui still serves clients, then stops 3x-ui and installs still-ui with a copy of its database (an installer that has not finished after 10 minutes, `STILL_UI_MIGRATE_INSTALL_TIMEOUT`, counts as failed). still-ui lives in its own folders (`/usr/local/still-ui`, `/etc/still-ui`, `still-ui.service`); ports, certificates, the panel base path, subscription URLs, client links and the panel login (with 2FA) come from the database and stay the same.
5. Checks still-ui: the panel answers at the same address, Xray runs, inbounds, clients, login and listener settings match, and the sampled subscriptions are byte-identical. A 3x-ui 2.x writes fewer link parameters than still-ui does; then every parameter a link had must still be there with the same value.
6. If any step after stopping 3x-ui fails, it restores 3x-ui on its own and says why.

When it is done, `x-ui.service` is removed (it is in the backup) so 3x-ui cannot start next to still-ui, the `x-ui` command opens still-ui's menu, and acme.sh renewals that restarted `x-ui` restart `still-ui` (the rollback puts them back). 3x-ui's own files and database are not modified.

Undo it at any time — this restores 3x-ui's files, unit and database from the newest backup, starts 3x-ui, removes still-ui and keeps still-ui's database in the backup folder:

```bash
still-ui migrate-rollback
```

Changes made in still-ui after the migration (new clients, traffic counters) do not travel back to 3x-ui. Without a terminal, set `STILL_UI_MIGRATE_YES=1`. A PostgreSQL setup needs `pg_dump` and `pg_restore` on the server. Tested on Debian 12 and Ubuntu 24.04 with 3x-ui 3.11.0 (SQLite and PostgreSQL), 3.7.0, 2.9.4, 2.6.7, 2.4.11, 2.3.15, 2.2.8 and 2.0.2 (`scripts/test-migrate.sh`).

A plain install also offers to take over `/etc/x-ui/x-ui.db` (non-interactive: `STILL_UI_IMPORT_3XUI=yes`), without the checks and the backup above. It stops 3x-ui, moves its unit to `/root/still-ui-3x-ui-retired/` and points acme.sh renewals at still-ui too; `bash /root/still-ui-3x-ui-retired/migrate.sh restore-import /root/still-ui-3x-ui-retired` puts both back (`still-ui uninstall --purge` runs it; an uninstall that keeps the data for a reinstall leaves 3x-ui retired).

The panel and the subscription server pick up a renewed certificate file within half a minute, without a restart.

## Moving from Marzban or x-ui

Install still-ui (on the Marzban server or on a new one with Marzban's data folder copied to it), then:

```bash
still-ui import marzban /var/lib/marzban -dry-run   # report only, writes nothing
still-ui import marzban /var/lib/marzban            # backup, import, restart
```

The argument is Marzban's data folder or its `db.sqlite3`; the inbounds are read from `xray_config.json` beside it (`-config FILE` names another one). Both files are only read. **Settings → Import** in the panel does the same: upload the two files, read the report, confirm.

What comes over:

- **Inbounds** — VLESS, VMess, Trojan and Shadowsocks on any transport, with TLS or REALITY, as Xray had them: port, REALITY private key and short IDs, paths. Certificate files must be readable at the same paths on this server; an inbound whose certificate is missing is listed and left out.
- **Users** — UUIDs and passwords, flow, traffic used and limit, expiry, note and state: a disabled user stays disabled, a limited or expired one stays blocked until renewed, a user on hold starts counting on the first connection. Daily, weekly and monthly traffic resets carry over onto still-ui's calendar — daily at midnight, weekly on Sunday, monthly on the day of the month the user's next Marzban reset was due (Marzban counted 1, 7 and 30 days from each user's own last reset); a yearly one does not.
- **Hosts** — address, port, SNI, host header, path, ALPN and fingerprint, as the hosts of the inbound. A host whose address is `{SERVER_IP}` uses the address the subscription is fetched from.
- **Subscription links** — Marzban's `/sub/<token>` links are accepted as they are, each user's revoke date included. They are served at `/sub/` whatever still-ui's own subscription path is, and only they: a plain subscription ID still needs the panel's own path. Point the address users have (host and port) at still-ui's subscription server; if Marzban had another `XRAY_SUBSCRIPTION_PATH`, set that as the subscription path in **Settings → Subscription**. To check these links still-ui keeps Marzban's signing secret in its own database. Whoever has that secret — an old Marzban backup, a former admin — can open imported users' subscriptions through such links, so once users have moved to the links from still-ui, run `still-ui import forget-links`: it stops accepting the old links and deletes the secret. Each imported user's own still-ui subscription ID is random and cannot be worked out from the secret.

What does not:

- Admins (the report lists them), nodes, user templates, and the `/info`, `/usage` and client-type paths under a subscription link.
- A client here has one UUID and one password, while Marzban gives each protocol its own. A user with both VLESS and VMess inbounds, or both Trojan and Shadowsocks, becomes two clients in one subscription (`name` and `name.vmess` or `name.ss`), each with the full limit and its own traffic counter.
- Link names: still-ui names links by its own remark template, not by the host remark. Every connection setting is the same.
- Marzban on MySQL or MariaDB: only the SQLite file is read.

`still-ui import x-ui /etc/x-ui/x-ui.db` does the same for x-ui (vaxilu/x-ui and its forks) and 3x-ui before v3: inbounds with their clients and traffic; a client without an email gets a name. For 3x-ui 2.0 and later prefer [the migration above](#moving-from-3x-ui), which also keeps the settings and the panel login.

A panel that already has inbounds needs `-merge`: inbounds whose port or tag is taken and clients whose email exists or whose subscription ID belongs to another client here are skipped and listed. To undo an import, restore the backup it printed: `still-ui restore <file>`.

Tested end to end in Docker (`scripts/test-import-marzban.sh`): Marzban v0.8.4 from its official image with VLESS REALITY, VMess WebSocket and Shadowsocks inbounds, then still-ui in its place; an Xray client connects with the links and the subscription link Marzban gave, unchanged.

## Check reachability from Russia

A panel cannot tell from the inside whether its inbounds work for users in a censored network: the server sees a handshake, while the censor may freeze the connection a few kilobytes later. A **probe** answers "does my VPN work from there right now?". It is a small agent (`still-probe`, a static binary with the still-ui Xray) that you run on any Linux box inside that network: a home server, a Raspberry Pi or a cheap local VPS.

1. In the panel open **Probes** → **Add probe**, give it a name and choose the inbounds to test (VLESS, VMess, Trojan, Shadowsocks, Hysteria). The panel creates a service client `still-probe-…` for it on those inbounds, with no traffic or time limit; do not edit or delete that client.
2. Copy the install command shown once with the token and run it as root on the probe box (amd64 or arm64, systemd):

```bash
STILL_PROBE_TOKEN='<token>' bash <(curl -fsSL https://raw.githubusercontent.com/Dv2Corn/still-ui/main/scripts/still-probe-install.sh) \
    --panel 'https://<server>:<port>/<base path>/'
```

The token is passed in the environment, which other local users cannot read; `--token-file FILE` works too, and without either the installer asks for it. `--token '<token>'` still works but prints a warning: a command line is visible to every user in `ps`.

The installer downloads `still-probe-linux-<arch>.tar.gz` from the latest still-ui release and checks its sha256, installs the agent to `/usr/local/still-probe`, its config to `/etc/still-probe/config.json` (root:still-probe 0640) and a systemd timer, and runs the first check. Add `--pin <sha256>` for a panel with a self-signed certificate, `--uninstall` to remove everything. An `http://` panel address is refused: the token, the panel's secret path and the keys of every tested inbound would cross the censored network in clear text. `--insecure-http` accepts that anyway and prints a warning.

Every interval (10 minutes by default) the agent fetches its job from the panel, starts Xray with one SOCKS port per inbound endpoint (the same address, SNI and keys the subscription hands out), downloads the test URL through each of them (by default 256 KiB from Cloudflare: a censor that cuts connections after the first kilobytes shows up as a failure) and, optionally, a direct URL without the VPN, such as your site or subscription page. It reports success, time to first byte, total time and bytes. If the panel itself is blocked from there, the report goes through an inbound that just worked.

The **Probes** page shows the last result of every endpoint, its uptime over 7 days and an hourly history strip; a probe that has not reported for three intervals is marked "Not reporting". The Telegram bot sends an alert when an endpoint fails twice in a row, when it works again, and when a probe goes silent: turn on the "Reachability probes" events in **Panel Settings → Telegram**.

Subscriptions can use the probe results too (**Subscription → Use probe results**, off by default). With **Move to the end**, a link whose endpoint the probes report down goes to the end of every format (links, JSON, Clash); with **Hide**, it is left out, and Clash groups and JSON balancers stop naming it. An endpoint counts as down only after two failed checks in a row on enough probes: by default 1 when only one probe checks that inbound, otherwise 2. Results of a probe that has not reported within the expiry window (30 minutes by default) are ignored, a probe that fails on every endpoint at once is ignored when its own check without the VPN fails too, or, if it has no such check, when the failing endpoints are on more than one server (its own network is the likely cause); one whole server failing on a probe whose direct check passes is believed, because that is what a blocked server address looks like; and an endpoint without results stays where it is. A user always keeps at least one link: when every link is down the normal list is served. The Probes page shows which endpoints subscriptions move down or hide. The panel's own link and QR views and the subscription web page always show every link.

The probe token opens only the two agent routes (`<base path>probe/v1/job` and `probe/v1/report`), nothing under `/panel/api`; the panel stores only its SHA-256, and a job holds only the probe client's own credentials. **New token** revokes the old token and also gives the probe client new keys, so a leaked agent config or link stops working; a disabled or deleted probe has no client at all. WireGuard, AmneziaWG, TUIC and MTProto inbounds are not probed yet.

## Check your panel

[xui-doctor](https://github.com/Dv2Corn/xui-doctor) is a small read-only checker for still-ui and 3x-ui. It looks at the Xray core version, REALITY and TLS settings, open ports, clients and panel security, and prints what to fix. It changes nothing on the server.

```bash
go install github.com/Dv2Corn/xui-doctor/cmd/xui-doctor@latest
XUI_API_TOKEN=<token> xui-doctor check --url https://<server>:<port>/<base path>/
```

Exit code 0 means no problems, 1 means warnings, 2 means something critical. Add `--json` for scripts.

## Donate

If still-ui saves you time, you can support its development:

- USDT (TRC20): `TE18ocTtSRa855uCzKFnPFgnSAtJ5Jny2x`
- BTC: `bc1qneuy3u36lrln042uezc7a7ycxd30n5fk29f43e`

Card payments via DonationAlerts: https://www.donationalerts.com/r/dv2corn

A star on GitHub helps too.

## Thanks

- [3x-ui](https://github.com/MHSanaei/3x-ui) by MHSanaei and its contributors — still-ui is built on their work. You can support the 3x-ui author too: [GitHub Sponsors](https://github.com/sponsors/MHSanaei), [donate.sanaei.dev](https://donate.sanaei.dev/).
- [x-ui](https://github.com/vaxilu/x-ui) by vaxilu — where it all started — and [alireza0](https://github.com/alireza0/).
- [Xray-core](https://github.com/XTLS/Xray-core) by Project X — the engine underneath.

## License

still-ui is distributed under the [GNU General Public License v3.0](LICENSE), the same license as 3x-ui. If you distribute a modified version, you must publish your changes under the same license.
