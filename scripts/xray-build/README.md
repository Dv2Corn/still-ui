# still-ui's Xray build

Since 1.4.0, still-ui ships its own build of the Xray core instead of the
official release archive. It is upstream
[Xray-core v26.10.31](https://github.com/XTLS/Xray-core/releases/tag/v26.10.31)
with one change in the REALITY library it uses
([XTLS/REALITY@83f6217](https://github.com/XTLS/REALITY/commit/83f621788ca88b83179810b12baa7ae518aacb83)):
[`reality-accept-no-mlkem.patch`](reality-accept-no-mlkem.patch), one hunk in
`tls.go`. Nothing else differs from the upstream release.

## What the patch changes, and why

Since Xray 26.9.9, a REALITY server accepts a TLS Client Hello only if it
carries an X25519MLKEM768 key share. Every other Client Hello gets the real
target site, so the client cannot connect. That refuses:

- the uTLS fingerprints `ios`, `edge` and `qq` in Xray clients (they have no
  X25519MLKEM768 key share);
- every sing-box based client (Hiddify, Karing and others), with any
  fingerprint;
- mihomo (Clash Meta) clients unless `support-x25519mlkem768: true` is set.

`ios` matters more than it looks. Its Client Hello is about 517 bytes and fits
in one TCP segment; `chrome`, `firefox` and `safari` take 1,500–1,900 bytes and
two segments. Some mobile networks that run allow-lists drop a Client Hello
that does not fit in one segment, so for their users `ios` is the fingerprint
that works.

The patch keeps the upstream check that refuses a Client Hello with a
duplicated key share (two X25519MLKEM768 shares, or two X25519 shares), and
drops only the rule "no X25519MLKEM768 share → refuse". A Client Hello with an
X25519 share alone is accepted again, as in Xray 26.6.27 and earlier. Clients
that do send X25519MLKEM768 are served exactly as before.

## Reproducing the build

[`build.sh`](build.sh) pins everything:

| | |
| --- | --- |
| Xray-core | tag `v26.10.31`, commit `a4a3dff5b20c2faae567b742c172c7b95e75c083` |
| REALITY | commit `83f621788ca88b83179810b12baa7ae518aacb83` + the patch |
| Go | 1.27.3, image `golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b` |
| Flags | `CGO_ENABLED=0`, `-trimpath -ldflags "-s -w -buildid= -X github.com/xtls/xray-core/core.build=sp-ios"` |

The `-X …core.build=sp-ios` part only sets the build name that `xray version`
prints after the codename; it is kept because it is part of the binary and so
of the sums below.

On a Linux host with git and docker:

```bash
scripts/xray-build/build.sh /tmp/xray-out amd64 arm64
sha256sum /tmp/xray-out/xray-linux-*
```

Expected sha256:

| File | sha256 |
| --- | --- |
| `xray-linux-amd64` | `67dfd3a25b349adc128112864973a9f1c9f3f4b58a3541d4d447374b9e6301aa` |
| `xray-linux-arm64` | `7c5be31d6ec2a65c8d902eebf382d050c5a84d28d12325a8c0cd3da310c6af6d` |

The script fails if a build gives another sum. The arm64 binary is
cross-compiled with the same amd64 Go image, so the host does not change it.
`xray version` prints `Xray 26.10.31 (Xray, Penetrates Everything.) sp-ios
(go1.27.3 linux/amd64)`.

`scripts/still-release.sh` runs `build.sh` for every arch it packages, and the
Dockerfile runs it in its `xray` stage (armv7, armv6 and 386 images get the same
patched source; their sums are not pinned).

## Going back to the official core

On a running panel, the **Version** button next to Xray on the overview page installs any official Xray
release (the panel shows a warning that this replaces the still-ui build).
`still-ui update` puts the still-ui build back.

To build a still-ui release with the official core, replace the Xray step in
`scripts/still-release.sh` with a download of
`https://github.com/XTLS/Xray-core/releases/download/v26.10.31/Xray-linux-<64|arm64-v8a>.zip`
(that is what 1.3.0 and earlier did).

With an official Xray 26.9.9 or newer, REALITY clients must send an
X25519MLKEM768 key share: use the `chrome`, `firefox` or `safari` fingerprint,
not `ios`, `edge` or `qq`, and enable `support-x25519mlkem768` in mihomo.
sing-box clients cannot connect at all.
