[English](README.md) | [Русский](README.ru.md) | [فارسی](README.fa.md) | **中文**

# still-ui

<p>
  <a href="https://www.gnu.org/licenses/gpl-3.0.en.html"><img src="https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true" alt="许可证：GPL-3.0"></a>
  <a href="https://github.com/MHSanaei/3x-ui"><img src="https://img.shields.io/badge/based%20on-3x--ui%20v3.11.0-555" alt="基于 3x-ui v3.11.0"></a>
  <a href="https://github.com/Dv2Corn/still-ui/releases/latest"><img src="https://img.shields.io/github/v/release/Dv2Corn/still-ui?label=release" alt="最新版本"></a>
  <a href="https://github.com/Dv2Corn/still-ui/actions/workflows/ci.yml?query=branch%3Amain"><img src="https://github.com/Dv2Corn/still-ui/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/Dv2Corn/still-ui/attestations"><img src="https://img.shields.io/badge/builds-attested-2ea44f" alt="发布构建由 GitHub Actions 证明"></a>
</p>

**still-ui** 是 MHSanaei 开发的 [3x-ui](https://github.com/MHSanaei/3x-ui) 面板的一个分支（fork），用于管理 [Xray-core](https://github.com/XTLS/Xray-core)。它以 3x-ui **v3.11.0** 为基础，保留了原面板的全部能力：相同的协议、相同的界面、相同的数据库。

不同之处在于关注点。当面板在一台小服务器上连续运行几个月、用户大多使用移动网络时，一些问题会逐渐暴露出来：客户端设置悄无声息地丢失、握手在丢包严重的线路上失败、面板进程一点点吃光服务器内存。still-ui 专门解决这些问题。

名字本身就说明了目标：服务器配置一次，之后就不用再操心。

> [!IMPORTANT]
> still-ui 是一个独立分支，与 3x-ui 的作者没有任何关联，也未获得其背书。still-ui 的问题请在本仓库反馈，不要提交到 3x-ui。

## 有什么不同

### 已完成

- **部分修改客户端不再把客户端改坏。** 在 3x-ui v3.11.0 中，通过 API（`/panel/api/clients/update/:email`）更新客户端时如果只提交部分字段，客户端会被禁用（`enable=false`），`limitIp`、`totalGB`、`tgId`、`comment`、`group`、`flow`、续期设置（`reset`、`resetMax`）和 `limitHwid` 会被清零。在 still-ui 中，请求里没有的字段取自已保存的客户端。显式提交 0 或空值仍然会清空对应字段。WireGuard/AmneziaWG 的密钥和对端地址在 v3.11.0 中本来就会保留，有测试保证这一点。
- **Xray 核心重新支持 `ios` 指纹和 sing-box 客户端。** 从 Xray 26.9.9 起，官方 REALITY 服务端只接受带有 X25519MLKEM768 密钥交换的 ClientHello，因此使用 `ios`、`edge`、`qq` 指纹的 Xray 客户端、任何指纹的 sing-box 客户端（Hiddify、Karing），以及未开启 `support-x25519mlkem768` 的 mihomo 都无法连接。`ios` 指纹很短（约 517 字节，一个 TCP 分段），在会丢弃较长 ClientHello 的移动网络上只有它能用。still-ui 1.4.0 自带从源码构建的 Xray v26.10.31，只对 REALITY 打了一处补丁：没有该密钥交换的 ClientHello 重新被接受，重复的密钥交换仍被拒绝，其他不变。构建可复现：配方、补丁和 sha256 见 [scripts/xray-build](scripts/xray-build/README.md)。新建的 REALITY 入站与 3x-ui 一样默认使用 `chrome`。在面板中把 Xray 切换为官方版本（概览页 Xray 旁的 **版本** 按钮）会恢复旧行为；`still-ui update` 会装回 still-ui 的构建。
- **安装脚本为面板设置内存上限。** 服务会获得 systemd 限制：`MemoryMax` 为内存的 35%（最少 256 MiB，最多 1 GiB），`MemoryHigh` 为其 85%，并且禁止面板使用 swap。面板内存失控时由 systemd 重启它，而不是把整台服务器拖进 swap 直至卡死。如果服务器没有 swap 且内存不超过 2 GiB，安装脚本会提议为系统其余部分创建一个 2 GiB 的 `/swapfile`。
- **面板重启时 Xray 继续运行。** Xray 作为独立服务 `still-xray` 运行：非特权用户、只有 `CAP_NET_BIND_SERVICE` 和 `CAP_NET_ADMIN`（套接字标记、TPROXY）、`ProtectSystem=strict`、`Restart=always`，并有自己的内存上限。重启、升级面板或面板崩溃都不会断开客户端。面板仍通过 Xray API 增删客户端而无需重启，只有入站结构变化时才重启内核——且先用 `xray run -test` 检查配置；通过检查却无法启动的配置会被换回上一个可用配置。在所有带 systemd 的服务器上这都是默认行为；已有的安装会在下一次 `still-ui update` 时迁移过来，除非其配置需要该服务做不到的事（见下文）。Docker 镜像与 3x-ui 一样把 Xray 留在面板内部。内置的 AmneziaWG、MTProto 和 TUIC 仍运行在面板内部，面板重启时会重新连接。
- **定时加密备份与恢复命令。** 面板可以按计划把数据库备份到 `/etc/still-ui/backups`，保留最近 N 份，用你的 [age](https://age-encryption.org) 或 SSH 公钥加密（私钥从不接触服务器），并把加密后的副本发送到 Telegram。`still-ui restore <file>` 会检查备份、停止面板、替换数据库（保留旧数据库）并重新启动面板。
- **Telegram 健康告警。** Xray 未运行、Xray 重启过于频繁、面板或 Xray 内存接近上限、磁盘已满、证书即将过期、备份失败。问题出现时发一条消息，持续期间定期提醒，修复后再发一条。
- **自动加载续期后的证书。** Xray 每小时（或每 `ocspStapling` 秒）会自行重新读取 TLS 和 Hysteria 入站的证书文件。面板每分钟通过握手（TLS，Hysteria 用 QUIC）检查每个入站实际提供的证书；如果续期后内核在该时间内没有加载新文件（或入站使用 `oneTimeLoading`），或者正在提供的证书将在一小时内过期，只重启 Xray：使用 `still-xray` 服务时自动重启；Xray 在面板内部运行时（Docker，或 STILL_XRAY_UNIT=off），只有在 `/etc/default/still-ui` 中设置 `STILL_CERT_RESTART=on` 才会重启，因为这种重启会断开所有客户端；未设置时标签只显示证书已续期。概览页上的“证书”标签显示每个入站提供的证书及其有效期，并提前警告（提前一周；对于 IP 上的短期证书则提前其有效期的四分之一）。
- **AmneziaWG 转发缓冲区复用。** 经内置 AmneziaWG 的每个 TCP 连接都会新分配两个 32 KiB 复制缓冲区；现在改为从池中获取。仅转发部分：每个连接的分配约 69 KB → 2.9 KB。整个面板在 2 vCPU / 2 GiB 测试服务器上，经 AmneziaWG 每秒 100 个短连接、持续 5 分钟、各 3 次，3x-ui v3.11.0 对比 still-ui：负载下面板平均内存 403 → 176 MiB，峰值 525 → 211 MiB，空闲时相同。方法、限制和原始数据：[bench/awg-relay-churn](bench/awg-relay-churn/README.md)。
- **Clash/Mihomo 订阅中的 Auto 组。** 订阅有两台或更多服务器时，Clash/Mihomo 配置会多出一个 `Auto` 组：应用通过隧道测试每台服务器并自动切换到可用的一台（最快的一台，容差 50 ms，每 300 秒检测一次；或第一台有响应的）。`Auto` 是 `PROXY` 中的默认选择，仍可手动选服务器。新安装默认开启；已有面板请在订阅设置中开启 **Auto group**。
- **订阅参考可达性探测结果。** 探测报告为不可用（连续两次失败）的入口，其链接可以移到订阅末尾或隐藏，适用于所有格式：链接、JSON 和 Clash。用户至少保留一条链接；没有新的探测结果时不做任何改动。默认关闭。
- **客户端出站过滤。** 可以禁止客户端经 25 端口发信（新安装默认开启）、访问服务器后面的私有网络和服务器自身的面板与 SSH 端口，以及你列出的地址和端口。对经 Xray 的所有客户端入站生效，有 `nft` 时也对内核模式的 AmneziaWG 客户端生效；主面板可以把过滤规则下发给节点。
- **每个节点的月流量上限。** 主面板按月（从你选定的重置日起）统计节点自身入站的流量，在 80%（可调）和达到上限时发送 Telegram 告警，并可在下一周期前关闭该节点的入站。计费系数（0.1 到 100）按比例换算该节点计入客户端统计的流量，从修改时起生效（之前的流量按原系数计）。节点按每个客户端所属主面板的计数检查其配额，因此系数不同的主面板可以共用一个节点。默认不设上限。
- **一条命令添加节点。** **节点 → 用命令添加** 会生成一次性邀请（默认 30 分钟有效，可撤销）；在新服务器上以 root 运行面板给出的命令，即可从发行版安装 still-ui、把它设为节点并加入主面板。无需手动复制 API 令牌和证书指纹；节点在做任何改动前会先校验主面板的密钥。[说明（英文）](docs/content/docs/en/operations/multi-node.mdx)。

### 进行中

- **面板数天内的内存。** AmneziaWG 大量连接下内存增长的原因已找到并修复（见上文）；还需在负载下进行多日测量，确认没有其他增长来源。

完整计划（俄文）见 [docs/ROADMAP.md](docs/ROADMAP.md)。

## 安装

以 root 身份运行：

```bash
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh)
```

如需安装指定版本，在末尾加上版本标签（标签格式为 `vX.Y.Z-still.N`）：

```bash
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh) v1.4.0
```

使用已下载的发行版压缩包安装：

```bash
STILL_UI_ARCHIVE=/path/still-ui-linux-amd64.tar.gz bash install.sh
```

安装脚本会随机生成用户名、密码和面板访问路径，并在结束时显示出来，请妥善保存。之后运行 `still-ui` 即可打开管理菜单：启动和停止服务、查看或重置登录信息、修改端口、管理证书等。子命令与 3x-ui 的 `x-ui` 脚本相同：`still-ui start`、`stop`、`restart`、`status`、`log`、`update`、`legacy`、`uninstall` 等。

发行版只提供 **Linux amd64 和 arm64** 的构建。当前版本：still-ui **1.6.1**（基于 3x-ui 3.11.0 及其后合并的修复），内置 still-ui 自行构建的 **Xray v26.10.31**（[有何不同](scripts/xray-build/README.md)）。

### 文件位置

| 内容 | 位置 |
| --- | --- |
| 服务 | `still-ui`（`systemctl status still-ui`） |
| 程序 | `/usr/local/still-ui` |
| 数据库 | `/etc/still-ui/x-ui.db`（文件名沿用 3x-ui） |
| 日志 | `/var/log/still-ui` |
| 配置 | `/etc/default/still-ui` |
| Xray | 服务 `still-xray`，配置 `/etc/still-xray`，日志 `/var/log/still-xray` |
| 备份 | `/etc/still-ui/backups`（可用配置文件中的 `XUI_BACKUP_FOLDER` 更改位置） |

### 内存上限

查看当前上限：

```bash
systemctl show still-ui -p MemoryMax
```

如需自定义，在 `/etc/default/still-ui` 中加一行，例如 `STILL_UI_MEMORY_MAX=800M`；写 `STILL_UI_MEMORY_MAX=off` 则取消上限。

### 面板重启期间 Xray 保持运行

Xray 作为 systemd 服务 `still-xray` 独立于面板运行。`systemctl restart still-ui`、面板升级（通过命令行或更新按钮）以及面板崩溃都不会影响它，因此客户端保持连接。已在一次性 Debian 12 容器中测试：通过一个 SOCKS 入站以 400 KB/s 下载 20 MB 文件，中途分别执行 `systemctl restart still-ui`、对面板执行 `kill -9` 以及一次完整的 `still-ui update`——每次文件都完整到达，Xray 进程始终是同一个。

Xray 什么时候会重启？入站结构变化时（面板会提示）、点击 **重启 Xray** 时、执行 `still-ui restore` 之后，以及安装迁移到该服务时的那一次。升级带来的新 Xray 只有在重启 Xray 后才会运行；概览页会提示这一点。

检查：`systemctl status still-xray`。使用该服务期间，配置文件 `/etc/default/still-ui` 中有 `XUI_XRAY_UNIT=still-xray`。

升级时，`x-ui xray-unit-check` 会先检查当前配置。如果某个入站符合下列情况，Xray 会留在面板内部，升级过程会打印原因：
- 监听 `/run/still-xray/` 和 `/dev/shm/` 以外的 unix 套接字（该服务有自己独立的 `/tmp`，其他路径对它都是只读的；可改用例如 `/dev/shm/xray.sock,0666`），
- 是 TUN 入站：它需要 `/dev/net/tun`，而服务的沙箱隐藏了它；在 `/etc/systemd/system/still-xray.service.d/` 中用你自己的 drop-in 设置 `PrivateDevices=no` 即可允许，然后重新运行升级。

TPROXY 和套接字标记（策略路由、WARP）在该服务中可用：它拥有 `CAP_NET_ADMIN`。正在运行的服务做不到的更改会被面板拒绝并说明原因，例如本地 drop-in 去掉了该权限后的套接字标记；若内核仍是在服务获得该权限的升级之前启动的，需要它的更改会让内核重启一次。

`/etc/default/still-ui` 中的设置：`STILL_XRAY_UNIT=off` 把 Xray 留在面板内部（与 3x-ui 相同），`STILL_XRAY_UNIT=on` 强制使用该服务，不设置则自动判断。`STILL_XRAY_MEMORY_MAX` 为内核设置内存上限（默认内存的一半，256M 到 4G）。修改后运行 `still-ui update`。Docker 镜像没有 systemd，始终把 Xray 留在面板内部。

### 备份与恢复

面板中的 **面板设置 → 备份**：

- 打开 **定时备份**，并设置 **计划**（默认每天）。面板会创建一致性快照（SQLite backup API；PostgreSQL 使用 `pg_dump`），确认能够读回，然后把 `still-ui-<host>-<date>-<time>.db`（`.pgdump`）写入 `/etc/still-ui/backups`（文件 0600，目录 0700）。修改计划后请重启面板。
- **保留份数**——每次新备份之后删除更旧的副本；目录中的其他文件永远不会被动。
- **加密密钥**——age 公钥（`age1…`，在你自己的电脑上用 `age-keygen` 生成）或 SSH 公钥（`ssh-ed25519 …`），每行一个。设置了密钥后，备份会写成 `….db.age`，不会保存任何未加密的内容。私钥留在你手中。每日发送到 Telegram 的备份（**Telegram 机器人 → 通知 → 数据库备份**）也会用同样的密钥加密，Xray 配置也是如此。
- **发送到 Telegram**——每份加密备份也会发给 Telegram 管理员（最大 50 MB）。未加密的备份永远不会通过这种方式发送。
- **立即备份** 以及服务器上的备份列表。命令行：`still-ui backup`。

恢复：

```bash
still-ui restore /etc/still-ui/backups/still-ui-<host>-<date>-<time>.db.age
```

对于加密备份，脚本会请你粘贴私钥（`AGE-SECRET-KEY-1…`）；私钥通过管道传给面板程序，不会被保存。也可以把密钥文件作为第二个参数传入（age 身份文件或未加密的 OpenSSH 私钥）——只在恢复时把它复制到服务器上。备份会先被解密并检查（`PRAGMA integrity_check`、`pg_restore --list`）；密钥错误或文件损坏不会改变任何东西。随后面板停止，数据库被替换，旧数据库保存为 `/etc/still-ui/x-ui.db.before-restore-<time>`，Xray 重启一次以加载恢复后的入站，面板重新启动。使用 PostgreSQL 时，旧数据库会以 dump 的形式保存在旁边。

也可以在其他地方解密：`age -d -i key.txt backup.db.age > x-ui.db`。

### 告警

**面板设置 → Telegram 机器人 → 通知 → 服务器状态**（机器人必须已开启）：

| 告警 | 触发条件 | 默认值 |
| --- | --- | --- |
| Xray 未运行 | Xray 连续两次检查（一分钟）都未运行，且不是你停止的 | 开启 |
| Xray 重启 | Xray 在 M 分钟内崩溃或被 systemd 重启 N 次 | 10 分钟内 3 次 |
| 面板或 Xray 内存 | 面板或 Xray 使用了其 systemd `MemoryMax` 的 X%（不含文件缓存），没有上限时按内存总量计算；连续三次检查；低于阈值 15 个百分点连续三次后解除 | 90% |
| 磁盘已满 | `/`、数据库或备份所在的磁盘已用 X%；低于阈值 5 个百分点连续三次后解除 | 90% |
| 证书即将过期 | 面板或订阅的证书文件，或 TLS/Hysteria 入站实际提供的证书，将在 N 天内过期——最多为其有效期的四分之一，因此 6 天的 IP 证书只在最后一天半才会报告 | 7 天 |
| 备份失败 | 定时备份（或其 Telegram 上传）失败 | 开启 |

每分钟检查一次。问题出现时发送一次告警，持续期间每隔 **提醒间隔（小时）** 再发一次（默认 6，0 表示从不），修复后再发一次。状态保存在内存中：面板重启后，仍然存在的问题会再报告一次。

## 升级、回滚、卸载

升级到最新版本——使用下面的命令，或点击面板中的更新按钮：

```bash
still-ui update
```

回滚到之前的版本——`still-ui legacy` 会询问版本号；也可以带上对应标签运行安装脚本。数据库会保留。

```bash
still-ui legacy
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh) v<tag>
```

回滚到没有 `still-xray` 服务的版本（例如 `v3.11.0-still.1`）：`still-ui legacy` 会在安装前停止该服务；如果手动运行旧版安装脚本，请先执行 `systemctl disable --now still-xray`。

升级或回滚前请先备份 `/etc/still-ui`：旧版本可能无法读取新版本写入过的数据库。

卸载面板：

```bash
still-ui uninstall
```

该命令会删除程序、两个服务、`still-xray` 用户和管理菜单，但保留数据：`/etc/still-ui` 中的数据库、备份和 `install-result.env`，以及配置文件（`/etc/default/still-ui`）。再次安装 still-ui 时会沿用这些数据，登录信息、入站和客户端都保持不变。菜单会询问 “Also delete all data?”（默认否）；没有终端时永远不会删除数据。在脚本中：`still-ui uninstall --keep-data` 保留数据，`still-ui uninstall --purge` 连同数据一起删除（并提示是否清除本地 PostgreSQL）。与 3x-ui 不同，除非你明确要求，否则不会删除任何数据。

## 移动网络

**添加入站** → **预设** 中有两个面向移动网络的预设：TCP 443 上使用 `ios` 指纹的 VLESS REALITY，以及 UDP 443 上的 Hysteria2。**Xray 配置 → 路由 → 基本路由 → 阻止客户端使用种子下载** 选项（默认关闭）会在 Xray 中阻止客户端的 BitTorrent 流量，在装有 nftables 的 Linux 上还会在服务器本身进行阻止。完整说明（英文）：[Mobile networks in Russia](README.md#mobile-networks-in-russia)。

## 验证发行版

发行版压缩包由 GitHub Actions 从打了标签的提交构建（[release 工作流](.github/workflows/release.yml)），而不是在开发者的机器上构建，每个压缩包都附带经过签名的[构建来源证明（build provenance attestation）](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations)。1.4.0 及之前的版本是用 `scripts/still-release.sh` 手工构建的，只有 `.sha256` 文件。

| 文件 | 说明 |
| --- | --- |
| `still-ui-linux-<arch>.tar.gz` | `install.sh` 和 `still-ui update` 下载的压缩包 |
| `still-ui-linux-<arch>.tar.gz.sha256` | 它的校验和；安装脚本会核对 |
| `still-ui-linux-<arch>.tar.gz.spdx.json` | SBOM（SPDX）：压缩包中的每个文件及其 sha256，以及 `x-ui`、`xray` 和 `mtg` 内含的 Go 模块 |
| `still-probe-linux-<arch>.tar.gz`（及 `.sha256`） | 可达性检查代理程序，由 `scripts/still-probe-install.sh` 下载；与面板压缩包一样带有来源证明 |
| `SHA256SUMS` | 压缩包、SBOM 和检查代理压缩包的校验和 |

下载并核对校验和（把 `v1.6.1` 换成你要安装的版本）：

```bash
gh release download v1.6.1 -R Dv2Corn/still-ui -p 'still-ui-linux-amd64.tar.gz*' -p SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

确认压缩包是由本仓库的 release 工作流从该标签构建的（需要 [GitHub CLI](https://cli.github.com/)）：

```bash
gh attestation verify still-ui-linux-amd64.tar.gz -R Dv2Corn/still-ui \
  --signer-workflow Dv2Corn/still-ui/.github/workflows/release.yml --source-ref refs/tags/v1.6.1
```

SBOM 同样附有证明：

```bash
gh attestation verify still-ui-linux-amd64.tar.gz -R Dv2Corn/still-ui --predicate-type https://spdx.dev/Document/v2.3
```

压缩包中的 Xray 内核可以逐位重新构建。在装有 git 和 docker 的 Linux 主机上：

```bash
tar -xzf still-ui-linux-amd64.tar.gz still-ui/bin/xray-linux-amd64
sha256sum still-ui/bin/xray-linux-amd64
git clone https://github.com/Dv2Corn/still-ui && cd still-ui && git checkout v1.6.1
scripts/xray-build/build.sh /tmp/xray-out amd64 arm64
```

`build.sh` 会打印它构建出的二进制文件的校验和，如果与脚本中固定的值不同就会失败；压缩包中 `xray-linux-amd64` 的校验和必须与之相同。[Xray 可复现构建](.github/workflows/xray-reproducible.yml) 工作流会在配方每次变更时以及每周一次，分别在一台 x86_64 和一台 arm64 runner 上执行这一检查。补丁改了什么：[scripts/xray-build](scripts/xray-build/README.md)。

压缩包整体并不能逐位复现：面板二进制文件使用构建时当前的 C 工具链链接，geo 文件和 `mtg` 是各自项目当时的最新版本。把压缩包与提交和工作流运行关联起来的，是构建来源证明。

## 从 3x-ui 迁移

一条命令即可把正在运行的 3x-ui 迁移到 still-ui，并保留客户端和你所依赖的一切：

```bash
bash <(curl -Ls https://github.com/Dv2Corn/still-ui/releases/latest/download/install.sh) migrate
```

它会做这些事：

1. 找到 3x-ui（`/usr/local/x-ui`、`x-ui.service`、SQLite 数据库或 `/etc/default/x-ui` 中指定的 PostgreSQL 数据库），并拒绝无法安全迁移的情况：其他 x-ui 分支、早于 2.0.0 的 3x-ui（请先用 `x-ui update` 升级）、比 still-ui 所基于的版本更新的 3x-ui、没有 systemd 的主机、Docker 安装、已经运行 still-ui 的主机。
2. 在 3x-ui 仍在运行时，记录必须保留下来的内容：入站、客户端、面板登录信息、面板和订阅地址，以及最多五个客户端由 3x-ui 提供的订阅内容。
3. 在做任何改动之前，把所有内容备份到 `/root/still-ui-migrate-<UTC time>/`（仅 root 可读）：数据库的一致性副本（或 `pg_dump`）、`/usr/local/x-ui`、unit 文件及其 drop-in、`/usr/bin/x-ui`、env 文件，以及带校验和的清单（manifest）。
4. 在 3x-ui 仍为客户端服务时安装 still-ui 所需的软件包，然后停止 3x-ui，并用其数据库的副本安装 still-ui（安装程序 10 分钟内未完成即视为失败，`STILL_UI_MIGRATE_INSTALL_TIMEOUT`）。still-ui 使用自己的目录（`/usr/local/still-ui`、`/etc/still-ui`、`still-ui.service`）；端口、证书、面板基础路径、订阅 URL、客户端链接和面板登录信息（含 2FA）都来自数据库，保持不变。
5. 检查 still-ui：面板在原地址响应，Xray 正在运行，入站、客户端、登录和监听设置一致，抽样的订阅逐字节相同。3x-ui 2.x 写入链接的参数比 still-ui 少；此时链接原有的每个参数都必须以相同的值保留。
6. 如果停止 3x-ui 之后的任何一步失败，它会自动恢复 3x-ui 并说明原因。

完成后，`x-ui.service` 会被删除（备份中保留一份），以免 3x-ui 与 still-ui 同时启动；`x-ui` 命令会打开 still-ui 的菜单，原本重启 `x-ui` 的 acme.sh 续期改为重启 `still-ui`（回滚会恢复原样）。3x-ui 自身的文件和数据库不会被修改。

随时可以撤销——下面的命令会从最新的备份恢复 3x-ui 的文件、unit 和数据库，启动 3x-ui，卸载 still-ui，并把 still-ui 的数据库保留在备份目录中：

```bash
still-ui migrate-rollback
```

迁移后在 still-ui 中做的改动（新客户端、流量计数）不会同步回 3x-ui。没有终端时，请设置 `STILL_UI_MIGRATE_YES=1`。使用 PostgreSQL 时，服务器上需要有 `pg_dump` 和 `pg_restore`。已在 Debian 12 和 Ubuntu 24.04 上测试，3x-ui 版本为 3.11.0（SQLite 和 PostgreSQL）、3.7.0、2.9.4、2.6.7、2.4.11、2.3.15、2.2.8 和 2.0.2（`scripts/test-migrate.sh`）。

普通安装也会提议接管 `/etc/x-ui/x-ui.db`（非交互方式：`STILL_UI_IMPORT_3XUI=yes`），但不包含上述检查和备份。它会停止 3x-ui，把它的单元文件移到 `/root/still-ui-3x-ui-retired/`，同样让 acme.sh 续期指向 still-ui；`bash /root/still-ui-3x-ui-retired/migrate.sh restore-import /root/still-ui-3x-ui-retired` 会把两者恢复原样（`still-ui uninstall --purge` 会自动执行；保留数据以便重装的卸载会让 3x-ui 保持停用）。

面板和订阅服务器会在半分钟内自动使用续期后的证书文件，无需重启。

## 从受审查网络内部检查可达性

面板无法从内部判断它的入站对身处受审查网络（例如伊朗或中国）的用户是否可用：服务器看到的是一次握手，而审查者可能在几 KB 之后就冻结连接。**探针**（probe）回答的是“我的 VPN 此刻在那里能用吗？”。它是一个小型代理程序（`still-probe`，带有 still-ui Xray 的静态二进制文件），你可以在任何国家、该网络内部的任何一台 Linux 机器上运行它：家用服务器、树莓派（Raspberry Pi）或当地的廉价 VPS。

1. 在面板中打开 **探针** → **添加探针**，为它命名并选择要测试的入站（VLESS、VMess、Trojan、Shadowsocks、Hysteria）。面板会在这些入站上为它创建一个服务客户端 `still-probe-…`，不限流量和时间；请不要编辑或删除该客户端。
2. 复制只显示一次、带有令牌的安装命令，在探针机器上以 root 身份运行（amd64 或 arm64，需要 systemd）：

```bash
STILL_PROBE_TOKEN='<token>' bash <(curl -fsSL https://raw.githubusercontent.com/Dv2Corn/still-ui/main/scripts/still-probe-install.sh) \
    --panel 'https://<server>:<port>/<base path>/'
```

安装脚本会从 still-ui 最新发行版下载 `still-probe-linux-<arch>.tar.gz` 并核对其 sha256，把代理程序安装到 `/usr/local/still-probe`，把配置安装到 `/etc/still-probe/config.json`（root:still-probe 0640），并安装一个 systemd 定时器，然后执行第一次检查。面板使用自签名证书时加上 `--pin <sha256>`，要卸载全部内容则使用 `--uninstall`。`http://` 面板地址会被拒绝：令牌、面板的秘密路径以及每个被测入站的密钥都会以明文穿过受审查的网络。`--insecure-http` 仍然允许这样做，并打印警告。

每个间隔（默认 10 分钟），代理程序会从面板获取任务，为每个入站端点启动一个带 SOCKS 端口的 Xray（使用与订阅下发的相同的地址、SNI 和密钥），通过每个端点下载测试 URL（默认从 Cloudflare 下载 256 KiB：在最初几 KB 后切断连接的审查会表现为失败），还可以选择不经 VPN 直接访问一个 URL，例如你的网站或订阅页面。它会报告是否成功、首字节时间、总时间和字节数。如果从那里无法访问面板本身，报告会通过刚刚测试成功的入站发送。

**探针** 页面显示每个端点的最新结果、7 天可用率和按小时的历史条；连续三个间隔没有报告的探针会被标记为“无报告”。当某个端点连续两次失败、恢复正常，或探针失联时，Telegram 机器人会发送告警：请在 **面板设置 → Telegram 机器人** 中开启“可达性探针”事件。

探针令牌只能访问两个代理程序路由（`<base path>probe/v1/job` 和 `probe/v1/report`），不能访问 `/panel/api` 下的任何内容；面板只保存其 SHA-256，任务中只包含探针客户端自己的凭据。**新令牌** 会吊销旧令牌，并为探针客户端生成新密钥，因此泄露的代理配置或链接会失效；被禁用或删除的探针根本没有客户端。WireGuard、AmneziaWG、TUIC 和 MTProto 入站暂不支持探测。

## 检查你的面板

[xui-doctor](https://github.com/Dv2Corn/xui-doctor) 是一个面向 still-ui 和 3x-ui 的小型只读检查工具。它检查 Xray 内核版本、REALITY 和 TLS 设置、开放的端口、客户端以及面板安全，并列出需要修复的地方。它不会改动服务器上的任何东西。

```bash
go install github.com/Dv2Corn/xui-doctor/cmd/xui-doctor@latest
XUI_API_TOKEN=<token> xui-doctor check --url https://<server>:<port>/<base path>/
```

退出码 0 表示没有问题，1 表示有警告，2 表示存在严重问题。在脚本中使用时加上 `--json`。

## 支持本项目

如果 still-ui 对你有帮助，欢迎支持后续开发：

- USDT (TRC20): `TE18ocTtSRa855uCzKFnPFgnSAtJ5Jny2x`
- BTC: `bc1qneuy3u36lrln042uezc7a7ycxd30n5fk29f43e`

通过 DonationAlerts 银行卡支付：https://www.donationalerts.com/r/dv2corn

在 GitHub 上点一颗星也是很大的支持。

## 致谢

- [3x-ui](https://github.com/MHSanaei/3x-ui) —— 感谢 MHSanaei 及所有贡献者，still-ui 建立在他们的工作之上。也可以支持 3x-ui 的作者：[GitHub Sponsors](https://github.com/sponsors/MHSanaei), [donate.sanaei.dev](https://donate.sanaei.dev/)。
- [x-ui](https://github.com/vaxilu/x-ui) —— 感谢一切的起点 vaxilu，以及 [alireza0](https://github.com/alireza0/)。
- [Xray-core](https://github.com/XTLS/Xray-core) —— 感谢 Project X 提供底层引擎。

## 许可证

still-ui 采用 [GNU General Public License v3.0](LICENSE) 许可证发布，与 3x-ui 相同。分发修改版本时，必须以同一许可证公开你的修改。
