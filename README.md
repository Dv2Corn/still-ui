**English** | [Русский](README.ru.md) | [فارسی](README.fa.md) | [中文](README.zh.md)

# still-ui

<p>
  <a href="https://www.gnu.org/licenses/gpl-3.0.en.html"><img src="https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true" alt="License: GPL-3.0"></a>
  <a href="https://github.com/MHSanaei/3x-ui"><img src="https://img.shields.io/badge/based%20on-3x--ui%20v3.11.0-555" alt="Based on 3x-ui v3.11.0"></a>
</p>

**still-ui** is a fork of [3x-ui](https://github.com/MHSanaei/3x-ui) by MHSanaei — the web panel for [Xray-core](https://github.com/XTLS/Xray-core). It starts from 3x-ui **v3.11.0** and keeps everything that panel can do: the same protocols, the same interface, the same database.

The difference is focus. still-ui fixes the things that hurt when a panel runs for months on a small server with real users on mobile networks: client settings that quietly get lost, handshakes that die on lossy links, and a panel process that slowly eats the server's memory.

The name says what we are after: a server you set up once and then leave alone.

> [!IMPORTANT]
> still-ui is an independent fork. It is not affiliated with or endorsed by the 3x-ui authors. Please report still-ui problems here, not upstream.

## What is different

### Already done

- **Partial client edits no longer break clients.** In 3x-ui v3.11.0, updating a client through the API (`/panel/api/clients/update/:email`) with only some of its fields disabled the client (`enable=false`) and reset `limitIp`, `totalGB`, `tgId`, `comment`, `group`, `flow`, the renewal settings (`reset`, `resetMax`) and `limitHwid`. In still-ui a field you do not send is taken from the stored client. An explicit zero or empty value still clears a field. WireGuard/AmneziaWG keys and peer addresses were already kept in v3.11.0; a test pins that.
- **The Xray core accepts the `ios` fingerprint and sing-box clients again.** Since Xray 26.9.9, an official REALITY server accepts only a ClientHello that carries an X25519MLKEM768 key share. That refuses Xray clients with the `ios`, `edge` and `qq` fingerprints, every sing-box client (Hiddify, Karing) with any fingerprint, and mihomo unless `support-x25519mlkem768` is on. `ios` is the short one (about 517 bytes, one TCP segment), and on mobile networks that drop longer ClientHellos it is the fingerprint that works. still-ui 1.4.0 ships Xray v26.10.31 built from source with a one-hunk REALITY patch: a ClientHello without that key share is accepted again, duplicated key shares are still refused, nothing else changes. The build is reproducible: recipe, patch and sha256 in [scripts/xray-build](scripts/xray-build/README.md). New REALITY inbounds still start with `chrome`, as in 3x-ui. Switching Xray to an official release from the panel (the **Version** button next to Xray on the overview page) brings the old behaviour back; `still-ui update` restores the still-ui build.
- **The installer puts a memory cap on the panel.** The service gets systemd limits: `MemoryMax` is 35% of RAM (at least 256 MiB, at most 1 GiB), `MemoryHigh` is 85% of that, and the panel is not allowed to use swap. A runaway panel is restarted by systemd instead of pushing the whole server into swap and freezing it. If the server has no swap and 2 GiB of RAM or less, the installer offers to create a 2 GiB `/swapfile` for the rest of the system.
- **Xray keeps running when the panel restarts.** Xray runs as its own service `still-xray`: an unprivileged user, only `CAP_NET_BIND_SERVICE`, `ProtectSystem=strict`, `Restart=always` and its own memory cap. Restarting, updating or crashing the panel does not drop clients. The panel still adds and removes clients through the Xray API without a restart, and restarts the core only when the inbound structure changes — after checking the config with `xray run -test`; a config that passes the check but does not start is replaced with the last working one. This is the default on every server with systemd; an existing install moves over on its next `still-ui update`, unless its config needs something the service cannot do (see below). Docker images keep Xray inside the panel, as in 3x-ui. The embedded AmneziaWG, MTProto and TUIC still live inside the panel and reconnect when it restarts.
- **Scheduled, encrypted backups and a restore command.** The panel can back up its database on a schedule to `/etc/still-ui/backups`, keep the last N copies, encrypt them to your [age](https://age-encryption.org) or SSH public key (the private key never touches the server) and send the encrypted copy to Telegram. `still-ui restore <file>` checks the backup, stops the panel, swaps the database (keeping the old one) and starts it again.
- **Health alerts in Telegram.** Xray not running, Xray restarting too often, panel or Xray memory near its limit, a full disk, a certificate about to expire, a failed backup. One message when it starts, a reminder while it lasts, one when it is fixed.
- **Renewed certificates are picked up automatically.** Xray rereads the certificate files of TLS and Hysteria inbounds by itself once an hour (or every `ocspStapling` seconds). Once a minute the panel does a handshake (TLS, or QUIC for Hysteria) to see which certificate each inbound really serves; if the core has not picked up a renewed file within that time (or the inbound uses `oneTimeLoading`), or the served certificate expires within an hour, only Xray is restarted: by itself with the `still-xray` service, and with Xray inside the panel (Docker, or STILL_XRAY_UNIT=off) only with `STILL_CERT_RESTART=on` in `/etc/default/still-ui`, because that restart drops every client; without it the tag just says the certificate was renewed. The “Certificates” tag on the overview page shows what each inbound serves and until when, and warns in advance (a week ahead, or a quarter of the lifetime for short-lived IP certificates).
- **AmneziaWG relay buffers are reused.** Every TCP connection through the embedded AmneziaWG allocated two fresh 32 KiB copy buffers; they now come from a pool. The relay alone: about 69 KB → 2.9 KB allocated per connection. The whole panel on a 2 vCPU / 2 GiB test server, 100 short connections per second through AmneziaWG for 5 minutes, 3 runs each, 3x-ui v3.11.0 vs still-ui: panel memory under load 403 → 176 MiB on average, peak 525 → 211 MiB, the same at rest. Method, limits and raw data: [bench/awg-relay-churn](bench/awg-relay-churn/README.md).

### In progress

- **Panel memory over several days.** The growth under AmneziaWG connection churn is found and fixed (see above); what is left is a multi-day measurement under load to confirm there is no other source of growth.

The full plan (in Russian) is in [docs/ROADMAP.md](docs/ROADMAP.md).

## Install

Run as root:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)
```

To install a specific release, pass its tag (tags look like `vX.Y.Z-still.N`):

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v1.4.0
```

To install from a release archive you already downloaded:

```bash
STILL_UI_ARCHIVE=/path/still-ui-linux-amd64.tar.gz bash install.sh
```

The installer generates a random username, password and panel path and prints them at the end — save them. After that, run `still-ui` to open the management menu: start and stop the service, show or reset the login, change the port, manage certificates and so on. The subcommands are the same as in 3x-ui's `x-ui` script (`still-ui start`, `stop`, `restart`, `status`, `log`, `update`, `legacy`, `uninstall` and others).

Release builds exist for **Linux amd64 and arm64** only. The current release is still-ui **1.4.0**, based on 3x-ui 3.11.0 and the fixes merged after it, with **Xray v26.10.31** in still-ui's own build ([what differs](scripts/xray-build/README.md)).

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
- uses TPROXY, a TUN inbound or a socket mark, which need `CAP_NET_ADMIN`: grant it in a drop-in of your own in `/etc/systemd/system/still-xray.service.d/` and run the update again.

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
| Panel or Xray memory | the panel or Xray uses X% of its systemd `MemoryMax` (without file cache), or of the RAM when there is no cap; three checks in a row | 80% |
| Disk full | the disk with `/`, the database or the backups is X% full | 90% |
| Certificate expires | the panel's or the subscription's certificate file, or the certificate a TLS/Hysteria inbound actually serves, expires within N days — at most a quarter of its lifetime, so a 6-day IP certificate is reported only in its last day and a half | 7 days |
| Backup failed | a scheduled backup (or its Telegram upload) failed | on |

Checks run every minute. An alert is sent once when the problem starts, again every **Remind every** hours while it lasts (6 by default, 0 = never), and once more when it is fixed. The state lives in memory: after a panel restart a problem that is still there is reported once again.

## Update, roll back, remove

Update to the latest release — from the command line or with the update button in the panel:

```bash
still-ui update
```

Roll back to an earlier release — `still-ui legacy` asks for the version; or run the installer with that tag. The database is kept.

```bash
still-ui legacy
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v<tag>
```

Rolling back to a release without the `still-xray` service (for example `v3.11.0-still.1`): `still-ui legacy` stops the service before installing; if you run an older installer by hand, run `systemctl disable --now still-xray` first.

Back up `/etc/still-ui` before updating or rolling back: an older version may not understand a database written by a newer one.

Remove the panel:

```bash
still-ui uninstall
```

> [!WARNING]
> `still-ui uninstall` also deletes the database in `/etc/still-ui`, just like 3x-ui does. Copy `/etc/still-ui/x-ui.db` somewhere safe first if you might need your inbounds and clients again.

## Mobile networks in Russia

Russian mobile operators inspect traffic (DPI), and in "whitelist" mode they cut most foreign TCP connections after the first 12–16 KB. Two things in still-ui help.

**Presets for new inbounds.** In **Add Inbound**, pick a **Preset**:

- **Mobile networks (Russia): VLESS REALITY** — VLESS on TCP 443 with REALITY, a fresh key pair and short IDs, and the `ios` fingerprint in the links and subscriptions of this inbound only (other new inbounds still start with `chrome`). The `ios` ClientHello is about 517 bytes and fits in one TCP segment; longer ones lose their second segment on some networks. still-ui's Xray accepts `ios`; official Xray 26.9.9 and newer refuses it. Then pick a target on the **Security** tab. **Find Targets** checks candidate sites from your server; prefer a site that is popular and not blocked in your clients' country over one well-known foreign site. Give clients the `xtls-rprx-vision` flow.
- **Mobile networks (Russia): Hysteria2 (UDP)** — Hysteria2 on UDP 443 with the `h3` ALPN and no Salamander obfuscation, so that most clients can connect. UDP often keeps working in whitelist mode when TCP is cut. Set the certificate on the **Security** tab.

**Block torrents for clients.** **Xray Configs → Routing → Basic Routing → Block torrents for clients.** It is off by default, and clients get no notice. When it is on:

- Every Xray config sends BitTorrent (as Xray's sniffer sees it) to a blackhole before any other rule. Inbounds with sniffing off get content sniffing without destination override, because Xray only recognises BitTorrent when it sniffs.
- On Linux with `nft`, the panel loads the nftables table `inet still_torrent_block`. It looks only at what the server sends out: connections the server itself opens, and traffic forwarded from AmneziaWG kernel-mode interfaces (`xui-awg*`), which never passes through Xray. Outgoing BitTorrent handshakes, DHT messages, UDP tracker connects and uTP SYNs are dropped; an AmneziaWG kernel-mode client that sent one keeps only ports 53/80/443/853 for 30 minutes (a client can only cut itself). A peer endpoint (address and port) is cut for 30 minutes only after that peer itself answers with a BitTorrent handshake or a DHT response, so a client cannot get some other site blocked by sending it a fake handshake. One risk remains: a TCP service that echoes what it receives back at the start of its reply can be made to send a 68-byte BitTorrent handshake, and then its own address and port are cut for 30 minutes. Answers from ports 53/80/443/853, from the panel, subscription, inbound, node and SSH ports, and from the server's own addresses are never learned, and each remote address can teach at most 30 endpoints a minute. The server's replies on connections that clients open to it are never inspected. The panel loads the table on start and again if it disappears; turning the option off removes it. Without `nft` (or not on Linux) only the Xray part works, and the page says so: install the `nftables` package. The Docker image includes it.

BitTorrent encrypted inside a client's own extra tunnel cannot be seen. Traffic that Xray passes on to another proxy is covered by the Xray rule only. With nodes, turn the option on in each node's own panel.

## Verify a release

Release archives are built by GitHub Actions from the tagged commit ([release workflow](.github/workflows/release.yml)), not on a developer's machine, and every archive comes with a signed [build provenance attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations). Releases up to and including 1.4.0 were built by hand with `scripts/still-release.sh` and have only the `.sha256` files.

| File | What it is |
| --- | --- |
| `still-ui-linux-<arch>.tar.gz` | the archive `install.sh` and `still-ui update` download |
| `still-ui-linux-<arch>.tar.gz.sha256` | its checksum; the installer checks it |
| `still-ui-linux-<arch>.tar.gz.spdx.json` | SBOM (SPDX): every file in the archive with its sha256, and the Go modules inside `x-ui`, `xray` and `mtg` |
| `still-probe-linux-<arch>.tar.gz` (+ `.sha256`) | the reachability probe agent that `scripts/still-probe-install.sh` downloads; attested like the panel archives |
| `SHA256SUMS` | checksums of the archives, SBOMs and probe archives |

Download and check the checksums (replace `v1.5.0` with the release you install):

```bash
gh release download v1.5.0 -R Dv2Corn/still-ui -p 'still-ui-linux-amd64.tar.gz*' -p SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

Check that the archive was built by this repository's release workflow from that tag (needs the [GitHub CLI](https://cli.github.com/)):

```bash
gh attestation verify still-ui-linux-amd64.tar.gz -R Dv2Corn/still-ui \
  --signer-workflow Dv2Corn/still-ui/.github/workflows/release.yml --source-ref refs/tags/v1.5.0
```

The SBOM is attested too:

```bash
gh attestation verify still-ui-linux-amd64.tar.gz -R Dv2Corn/still-ui --predicate-type https://spdx.dev/Document/v2.3
```

The Xray core inside the archive can be rebuilt bit for bit. On a Linux host with git and docker:

```bash
tar -xzf still-ui-linux-amd64.tar.gz still-ui/bin/xray-linux-amd64
sha256sum still-ui/bin/xray-linux-amd64
git clone https://github.com/Dv2Corn/still-ui && cd still-ui && git checkout v1.5.0
scripts/xray-build/build.sh /tmp/xray-out amd64 arm64
```

`build.sh` prints the sums of the binaries it built and fails if they differ from the ones pinned in it; the sum of `xray-linux-amd64` from the archive must be the same. The [Xray reproducible build](.github/workflows/xray-reproducible.yml) workflow does this on every change to the recipe and once a week, on an x86_64 and an arm64 runner. What the patch changes: [scripts/xray-build](scripts/xray-build/README.md).

The archive as a whole is not bit-for-bit reproducible: the panel binary is linked with the C toolchain current at build time, and the geo files and `mtg` are the latest releases of their projects at that moment. The provenance attestation is what ties the archive to the commit and the workflow run.

## Moving from 3x-ui

One command moves a running 3x-ui to still-ui and keeps everything clients and you rely on:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) migrate
```

What it does:

1. Finds 3x-ui (`/usr/local/x-ui`, `x-ui.service`, the SQLite database or the PostgreSQL one named in `/etc/default/x-ui`) and refuses what it cannot move safely: other x-ui forks, 3x-ui older than 2.0.0 (update it first with `x-ui update`), a 3x-ui newer than the release still-ui is based on, hosts without systemd, Docker installs, hosts that already run still-ui.
2. While 3x-ui still runs, records what has to survive: inbounds, clients, the panel login, the panel and subscription addresses, and the subscriptions of up to five clients as 3x-ui serves them.
3. Backs up everything to `/root/still-ui-migrate-<UTC time>/` (readable by root only) before changing anything: a consistent copy of the database (or a `pg_dump`), `/usr/local/x-ui`, the unit and its drop-ins, `/usr/bin/x-ui`, the env file, and a manifest with checksums.
4. Stops 3x-ui and installs still-ui with a copy of its database. still-ui lives in its own folders (`/usr/local/still-ui`, `/etc/still-ui`, `still-ui.service`); ports, certificates, the panel base path, subscription URLs, client links and the panel login (with 2FA) come from the database and stay the same.
5. Checks still-ui: the panel answers at the same address, Xray runs, inbounds, clients, login and listener settings match, and the sampled subscriptions are byte-identical. A 3x-ui 2.x writes fewer link parameters than still-ui does; then every parameter a link had must still be there with the same value.
6. If any step after stopping 3x-ui fails, it restores 3x-ui on its own and says why.

When it is done, `x-ui.service` is removed (it is in the backup) so 3x-ui cannot start next to still-ui, and the `x-ui` command opens still-ui's menu. 3x-ui's own files and database are not modified.

Undo it at any time — this restores 3x-ui's files, unit and database from the newest backup, starts 3x-ui, removes still-ui and keeps still-ui's database in the backup folder:

```bash
still-ui migrate-rollback
```

Changes made in still-ui after the migration (new clients, traffic counters) do not travel back to 3x-ui. Without a terminal, set `STILL_UI_MIGRATE_YES=1`. A PostgreSQL setup needs `pg_dump` and `pg_restore` on the server. Tested on Debian 12 and Ubuntu 24.04 with 3x-ui 3.11.0 (SQLite and PostgreSQL), 3.7.0, 2.9.4, 2.6.7, 2.4.11, 2.3.15, 2.2.8 and 2.0.2 (`scripts/test-migrate.sh`).

A plain install also offers to take over `/etc/x-ui/x-ui.db` (non-interactive: `STILL_UI_IMPORT_3XUI=yes`), without the checks and the backup above.

## Check reachability from Russia

A panel cannot tell from the inside whether its inbounds work for users in a censored network: the server sees a handshake, while the censor may freeze the connection a few kilobytes later. A **probe** answers "does my VPN work from there right now?". It is a small agent (`still-probe`, a static binary with the still-ui Xray) that you run on any Linux box inside that network: a home server, a Raspberry Pi or a cheap local VPS.

1. In the panel open **Probes** → **Add probe**, give it a name and choose the inbounds to test (VLESS, VMess, Trojan, Shadowsocks, Hysteria). The panel creates a service client `still-probe-…` for it on those inbounds, with no traffic or time limit; do not edit or delete that client.
2. Copy the install command shown once with the token and run it as root on the probe box (amd64 or arm64, systemd):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/Dv2Corn/still-ui/main/scripts/still-probe-install.sh) \
    --panel 'https://<server>:<port>/<base path>/' --token '<token>'
```

The installer downloads `still-probe-linux-<arch>.tar.gz` from the latest still-ui release and checks its sha256, installs the agent to `/usr/local/still-probe`, its config to `/etc/still-probe/config.json` (root:still-probe 0640) and a systemd timer, and runs the first check. Add `--pin <sha256>` for a panel with a self-signed certificate, `--uninstall` to remove everything.

Every interval (10 minutes by default) the agent fetches its job from the panel, starts Xray with one SOCKS port per inbound endpoint (the same address, SNI and keys the subscription hands out), downloads the test URL through each of them (by default 256 KiB from Cloudflare: a censor that cuts connections after the first kilobytes shows up as a failure) and, optionally, a direct URL without the VPN, such as your site or subscription page. It reports success, time to first byte, total time and bytes. If the panel itself is blocked from there, the report goes through an inbound that just worked.

The **Probes** page shows the last result of every endpoint, its uptime over 7 days and an hourly history strip; a probe that has not reported for three intervals is marked "Not reporting". The Telegram bot sends an alert when an endpoint fails twice in a row, when it works again, and when a probe goes silent: turn on the "Reachability probes" events in **Panel Settings → Telegram**.

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
