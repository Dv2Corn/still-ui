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
- **REALITY uses the `ios` fingerprint by default.** Its ClientHello is about 517 bytes and fits into a single TCP segment. Larger fingerprints (`chrome`, `firefox`) split the ClientHello into two packets, and on mobile networks that drop the second one the connection dies right at the handshake. With `ios` there is nothing to lose. You can still pick any other fingerprint per inbound.
- **The installer puts a memory cap on the panel.** The service gets systemd limits: `MemoryMax` is 35% of RAM (at least 256 MiB, at most 1 GiB), `MemoryHigh` is 85% of that, and the panel is not allowed to use swap. A runaway panel is restarted by systemd instead of pushing the whole server into swap and freezing it. If the server has no swap and 2 GiB of RAM or less, the installer offers to create a 2 GiB `/swapfile` for the rest of the system.
- **Xray as a separate service (opt-in).** With `STILL_XRAY_UNIT=on` in `/etc/default/still-ui`, the installer and `still-ui update` run Xray as its own service `still-xray`: an unprivileged user, only `CAP_NET_BIND_SERVICE`, `ProtectSystem=strict`, `Restart=always` and its own memory cap. The panel still adds and removes clients through the Xray API without a restart, and restarts the core only when the inbound structure changes — after checking the config with `xray run -test`; a config that passes the check but does not start is replaced with the last working one. Restarting, updating or crashing the panel no longer drops clients: tested by downloading 20 MB through the VPN with `systemctl restart still-ui` in the middle — the file arrived complete and the Xray process stayed the same. By default Xray stays the panel's child process, as in 3x-ui. The embedded AmneziaWG, MTProto and TUIC still live inside the panel and reconnect when it restarts.
- **Renewed certificates are picked up automatically.** Xray rereads the certificate files of TLS and Hysteria inbounds by itself once an hour (or every `ocspStapling` seconds). Once a minute the panel does a handshake (TLS, or QUIC for Hysteria) to see which certificate each inbound really serves; if the core has not picked up a renewed file within that time (or the inbound uses `oneTimeLoading`), or the served certificate expires within an hour, only Xray is restarted: by itself with the `still-xray` service, and with Xray inside the panel (the default) only with `STILL_CERT_RESTART=on` in `/etc/default/still-ui`, because that restart drops every client; without it the tag just says the certificate was renewed. The “Certificates” tag on the overview page shows what each inbound serves and until when, and warns in advance (a week ahead, or a quarter of the lifetime for short-lived IP certificates).
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
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v1.1.0
```

To install from a release archive you already downloaded:

```bash
STILL_UI_ARCHIVE=/path/still-ui-linux-amd64.tar.gz bash install.sh
```

The installer generates a random username, password and panel path and prints them at the end — save them. After that, run `still-ui` to open the management menu: start and stop the service, show or reset the login, change the port, manage certificates and so on. The subcommands are the same as in 3x-ui's `x-ui` script (`still-ui start`, `stop`, `restart`, `status`, `log`, `update`, `legacy`, `uninstall` and others).

Release builds exist for **Linux amd64 and arm64** only. The current release is still-ui **1.1.0**, based on 3x-ui 3.11.0 and the fixes merged after it, with **Xray v26.10.31**.

### Where things live

| What | Where |
| --- | --- |
| Service | `still-ui` (`systemctl status still-ui`) |
| Program | `/usr/local/still-ui` |
| Database | `/etc/still-ui/x-ui.db` (the file name is kept from 3x-ui) |
| Logs | `/var/log/still-ui` |
| Settings | `/etc/default/still-ui` |
| Xray (opt-in) | service `still-xray`, config `/etc/still-xray`, logs `/var/log/still-xray` |

### Memory cap

Check the current cap:

```bash
systemctl show still-ui -p MemoryMax
```

To set your own value, add a line to `/etc/default/still-ui` — for example `STILL_UI_MEMORY_MAX=800M`, or `STILL_UI_MEMORY_MAX=off` to remove the cap.

### Xray as a separate service

Turn it on: add `STILL_XRAY_UNIT=on` to `/etc/default/still-ui`, then run `still-ui update` (or the installer). Turn it off with `STILL_XRAY_UNIT=off` and `still-ui update` again. The core's memory cap is `STILL_XRAY_MEMORY_MAX` (half the RAM by default, 256M to 4G). Inbounds that need TPROXY, TUN or socket marks need `CAP_NET_ADMIN`/`CAP_NET_RAW` from a drop-in of your own in `still-xray.service.d`. Turning it on restarts Xray once; after that restarting or updating the panel leaves it alone, so a new Xray from an update runs only after you restart Xray (the overview says so). An inbound listening on a unix socket (a fallback from nginx) must use `/run/still-xray/` or `/dev/shm/`, e.g. `/dev/shm/xray.sock,0666`: the service has its own `/tmp` and everything else is read-only to it, so the panel refuses other paths and says why.

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

## Moving from 3x-ui

If the installer finds `/etc/x-ui/x-ui.db`, it offers to copy that database into still-ui and stop x-ui. Without a terminal (cloud-init and similar), set `STILL_UI_IMPORT_3XUI=yes` to accept. Your panel login, API access, inbounds and clients carry over; clients do not need new links. 3x-ui's own files are left untouched.

To go back, stop still-ui and start 3x-ui again:

```bash
systemctl disable --now still-ui
systemctl enable --now x-ui
```

This was tested with a 3x-ui v3.11.0 database. A database from a newer 3x-ui release may not open in still-ui.

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
