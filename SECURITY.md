# Security Policy

This policy covers still-ui, the fork published at
[Dv2Corn/still-ui](https://github.com/Dv2Corn/still-ui). still-ui is not
maintained by the 3x-ui authors; do not send still-ui reports to them.

## Reporting a vulnerability

Do not open a public issue, discussion or pull request for anything you
believe is exploitable: an authentication bypass, remote code execution,
injection, secret or credential exposure, privilege escalation, a way to make
the panel or the bundled Xray core serve something it should not. A public
report gives attackers a head start against every still-ui server.

Report it privately through GitHub instead: open the
[Security tab](https://github.com/Dv2Corn/still-ui/security) of this
repository and click **Report a vulnerability**
([direct link](https://github.com/Dv2Corn/still-ui/security/advisories/new)).
Only the maintainers see the report. Include:

- the still-ui version (`still-ui` menu, or `internal/config/version` for a
  source build) and whether Xray is still-ui's own build or an official one;
- the install method and OS;
- what an attacker needs (network access, a client link, a panel login) and
  what they get;
- steps to reproduce, and logs with secrets removed.

Replies come in the advisory thread. A fix ships in a new release; the
advisory is published once that release is available, crediting you unless you
ask not to be named. There is no bug-bounty program.

If the problem is in code still-ui shares with 3x-ui unchanged, you may also
report it to 3x-ui as their [security policy](https://github.com/MHSanaei/3x-ui/security/policy)
describes. If it is in Xray-core itself, report it to
[XTLS/Xray-core](https://github.com/XTLS/Xray-core/security). The REALITY patch
still-ui applies (`scripts/xray-build/reality-accept-no-mlkem.patch`) is
still-ui's to fix: report problems with it here.

## Supported versions

Only the latest release receives security fixes. Before reporting, update with
`still-ui update` and check that the problem is still there.

| Version | Supported |
| --- | --- |
| Latest release | Yes |
| Anything older | No |

## Verifying what you install

Releases are built by GitHub Actions and carry build provenance attestations,
SBOMs and checksums; the Xray core in them can be rebuilt bit for bit. See
"Verify a release" in the [README](README.md#verify-a-release).
