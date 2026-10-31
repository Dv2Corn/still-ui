**English** | [Русский](README.ru.md) | [فارسی](README.fa.md) | [中文](README.zh.md)

# still-ui

<p>
  <a href="https://www.gnu.org/licenses/gpl-3.0.en.html"><img src="https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true" alt="License: GPL-3.0"></a>
  <a href="https://github.com/MHSanaei/3x-ui"><img src="https://img.shields.io/badge/based%20on-3x--ui%20v3.11.0-555" alt="Based on 3x-ui v3.11.0"></a>
</p>

**still-ui** is a fork of [3x-ui](https://github.com/MHSanaei/3x-ui) by MHSanaei — the web panel for [Xray-core](https://github.com/XTLS/Xray-core). It starts from 3x-ui **v3.11.0** and keeps everything that panel can do: the same protocols, the same interface, the same database.

The difference is focus. still-ui fixes the things that hurt when a panel runs for months on a small server with real users on mobile networks: lost keys, handshakes that die on lossy links, and a panel process that slowly eats the server's memory.

The name says what we are after: a server you set up once and then leave alone.

> [!IMPORTANT]
> still-ui is an independent fork. It is not affiliated with or endorsed by the 3x-ui authors. Please report still-ui problems here, not upstream.

## What is different

### Already done

- **AmneziaWG keys survive client edits.** In 3x-ui, updating an AmneziaWG client without sending its `preSharedKey` silently wipes the key, and the client stops connecting. still-ui keeps the stored key unless you explicitly change it.
- **REALITY uses the `ios` fingerprint by default.** Its ClientHello is about 517 bytes and fits into a single TCP segment. Larger fingerprints (`chrome`, `firefox`) split the ClientHello into two packets, and on mobile networks that drop the second one the connection dies right at the handshake. With `ios` there is nothing to lose. You can still pick any other fingerprint per inbound.
- **The installer puts a memory cap on the panel.** The service gets a systemd `MemoryMax` limit, so a runaway panel is restarted by systemd instead of pushing the whole server into swap and freezing it.

### In progress

- **Fixing the panel's memory leak** — not just capping it, finding and removing the cause.
- **Xray as a separate service.** Restarting or updating the panel will no longer drop connected clients: Xray keeps running on its own.
- **Automatic pickup of renewed certificates.** When a certificate is renewed, inbounds start using it without a manual restart.

The full plan (in Russian) is in [docs/ROADMAP.md](docs/ROADMAP.md).

## Install

Run as root on a fresh server:

<!-- TODO: сверить с установщиком -->
```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)
```

To install a specific release, add its tag:

<!-- TODO: сверить с установщиком -->
```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) <version>
```

The installer generates a random username, password and panel path and prints them at the end — save them. After that, run `still-ui` to open the management menu: start and stop the service, show or reset the login, change the port, manage certificates and so on.

<!-- TODO: сверить с установщиком -->
```bash
still-ui
```

Supported systems and CPU architectures are the same as in 3x-ui v3.11.0.

## Update, roll back, remove

<!-- TODO: сверить с установщиком -->
```bash
still-ui update      # update to the latest release
```

<!-- TODO: сверить с установщиком -->
```bash
still-ui legacy      # install a specific older version (asks for the version number)
```

<!-- TODO: сверить с установщиком -->
```bash
still-ui uninstall   # remove the panel and its service
```

Other useful commands:

<!-- TODO: сверить с установщиком -->
```bash
still-ui status      # service status
still-ui restart     # restart the panel
still-ui log         # show logs
```

The service itself is called `still-ui`:

<!-- TODO: сверить с установщиком -->
```bash
systemctl status still-ui
systemctl show still-ui -p MemoryMax   # the memory cap set by the installer
```

Back up the database before any update or rollback. An older version may not understand a database written by a newer one.

## Moving from 3x-ui

still-ui uses the same database format as 3x-ui v3.11.0, so inbounds, clients and settings carry over. Clients do not need new links.

<!-- TODO: сверить с установщиком -->
1. Back up the 3x-ui database: `cp /etc/x-ui/x-ui.db /root/x-ui.db.backup`
2. Stop 3x-ui and turn off its autostart: `systemctl disable --now x-ui`
3. Install still-ui with the command above.
4. Put the database in place: `cp /root/x-ui.db.backup /etc/still-ui/still-ui.db`, then `systemctl restart still-ui`.
5. Log in with your old 3x-ui username and password and check your inbounds.

To go back, stop still-ui (`systemctl disable --now still-ui`) and start 3x-ui again (`systemctl enable --now x-ui`) — its database was not touched.

If your 3x-ui is older than v3.11.0, update it to v3.11.0 first and only then move.

## Support the project

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
