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

- **Partial client edits no longer break clients.** In 3x-ui v3.11.0, updating a client through the API (`/panel/api/clients/update/:email`) with only some of its fields disabled the client and reset `limitIp`, `tgId`, `comment` and other fields that were not in the request. In still-ui every field you do not send stays as it was — keys and client settings, including the AmneziaWG `preSharedKey`, are not lost. An explicit zero or empty value still clears a field.
- **REALITY uses the `ios` fingerprint by default.** Its ClientHello is about 517 bytes and fits into a single TCP segment. Larger fingerprints (`chrome`, `firefox`) split the ClientHello into two packets, and on mobile networks that drop the second one the connection dies right at the handshake. With `ios` there is nothing to lose. You can still pick any other fingerprint per inbound.
- **The installer puts a memory cap on the panel.** The service gets systemd limits: `MemoryMax` is 35% of RAM (at least 256 MiB, at most 1 GiB), `MemoryHigh` is 85% of that, and the panel is not allowed to use swap. A runaway panel is restarted by systemd instead of pushing the whole server into swap and freezing it. If the server has no swap and 2 GiB of RAM or less, the installer offers to create a 2 GiB `/swapfile` for the rest of the system.

### In progress

- **Fixing the panel's memory leak** — not just capping it, finding and removing the cause.
- **Xray as a separate service.** Restarting or updating the panel will no longer drop connected clients: Xray keeps running on its own.
- **Automatic pickup of renewed certificates.** When a certificate is renewed, inbounds start using it without a manual restart.

The full plan (in Russian) is in [docs/ROADMAP.md](docs/ROADMAP.md).

## Install

Run as root:

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)
```

To install a specific release, pass its tag (tags look like `vX.Y.Z-still.N`):

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v3.11.0-still.1
```

To install from a release archive you already downloaded:

```bash
STILL_UI_ARCHIVE=/path/still-ui-linux-amd64.tar.gz bash install.sh
```

The installer generates a random username, password and panel path and prints them at the end — save them. After that, run `still-ui` to open the management menu: start and stop the service, show or reset the login, change the port, manage certificates and so on. The subcommands are the same as in 3x-ui's `x-ui` script (`still-ui start`, `stop`, `restart`, `status`, `log`, `update`, `legacy`, `uninstall` and others).

Release builds exist for **Linux amd64 and arm64** only. The current release is panel **3.11.0-still.1** with **Xray v26.10.21**.

### Where things live

| What | Where |
| --- | --- |
| Service | `still-ui` (`systemctl status still-ui`) |
| Program | `/usr/local/still-ui` |
| Database | `/etc/still-ui/x-ui.db` (the file name is kept from 3x-ui) |
| Logs | `/var/log/still-ui` |
| Settings | `/etc/default/still-ui` |

### Memory cap

Check the current cap:

```bash
systemctl show still-ui -p MemoryMax
```

To set your own value, add a line to `/etc/default/still-ui` — for example `STILL_UI_MEMORY_MAX=800M`, or `STILL_UI_MEMORY_MAX=off` to remove the cap.

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

- USDT (TRC20): `<USDT_TRC20_ADDRESS>`
- USDT (TON): `<USDT_TON_ADDRESS>`
- BTC: `<BTC_ADDRESS>`
- ETH: `<ETH_ADDRESS>`

Card payments via DonationAlerts: <DONATIONALERTS_URL>

A star on GitHub helps too.

## Thanks

- [3x-ui](https://github.com/MHSanaei/3x-ui) by MHSanaei and its contributors — still-ui is built on their work.
- [x-ui](https://github.com/vaxilu/x-ui) by vaxilu — where it all started — and [alireza0](https://github.com/alireza0/).
- [Xray-core](https://github.com/XTLS/Xray-core) by Project X — the engine underneath.

## License

still-ui is distributed under the [GNU General Public License v3.0](LICENSE), the same license as 3x-ui. If you distribute a modified version, you must publish your changes under the same license.
