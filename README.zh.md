[English](README.md) | [Русский](README.ru.md) | [فارسی](README.fa.md) | **中文**

# still-ui

<p>
  <a href="https://www.gnu.org/licenses/gpl-3.0.en.html"><img src="https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true" alt="许可证：GPL-3.0"></a>
  <a href="https://github.com/MHSanaei/3x-ui"><img src="https://img.shields.io/badge/based%20on-3x--ui%20v3.11.0-555" alt="基于 3x-ui v3.11.0"></a>
</p>

**still-ui** 是 MHSanaei 开发的 [3x-ui](https://github.com/MHSanaei/3x-ui) 面板的一个分支（fork），用于管理 [Xray-core](https://github.com/XTLS/Xray-core)。它以 3x-ui **v3.11.0** 为基础，保留了原面板的全部能力：相同的协议、相同的界面、相同的数据库。

不同之处在于关注点。当面板在一台小服务器上连续运行几个月、用户大多使用移动网络时，一些问题会逐渐暴露出来：客户端设置悄无声息地丢失、握手在丢包严重的线路上失败、面板进程一点点吃光服务器内存。still-ui 专门解决这些问题。

名字本身就说明了目标：服务器配置一次，之后就不用再操心。

> [!IMPORTANT]
> still-ui 是一个独立分支，与 3x-ui 的作者没有任何关联，也未获得其背书。still-ui 的问题请在本仓库反馈，不要提交到 3x-ui。

## 有什么不同

### 已完成

- **部分修改客户端不再把客户端改坏。** 在 3x-ui v3.11.0 中，通过 API（`/panel/api/clients/update/:email`）更新客户端时如果只提交部分字段，客户端会被禁用（`enable=false`），`limitIp`、`totalGB`、`tgId`、`comment`、`group`、`flow`、续期设置（`reset`、`resetMax`）和 `limitHwid` 会被清零。在 still-ui 中，请求里没有的字段取自已保存的客户端。显式提交 0 或空值仍然会清空对应字段。WireGuard/AmneziaWG 的密钥和对端地址在 v3.11.0 中本来就会保留，有测试保证这一点。
- **REALITY 默认指纹改为 `ios`。** 它的 ClientHello 约 517 字节，可以完整放进一个 TCP 分段。较大的指纹（`chrome`、`firefox`）会把 ClientHello 拆成两个包；在会丢掉第二个包的移动运营商网络上，连接会直接断在握手阶段。用 `ios` 就不存在这个问题。每个入站仍然可以自行选择其他指纹。
- **安装脚本为面板设置内存上限。** 服务会获得 systemd 限制：`MemoryMax` 为内存的 35%（最少 256 MiB，最多 1 GiB），`MemoryHigh` 为其 85%，并且禁止面板使用 swap。面板内存失控时由 systemd 重启它，而不是把整台服务器拖进 swap 直至卡死。如果服务器没有 swap 且内存不超过 2 GiB，安装脚本会提议为系统其余部分创建一个 2 GiB 的 `/swapfile`。
- **Xray 作为独立服务（可选开启）。** 在 `/etc/default/still-ui` 中设置 `STILL_XRAY_UNIT=on` 后，安装脚本和 `still-ui update` 会把 Xray 作为独立服务 `still-xray` 运行：非特权用户、只有 `CAP_NET_BIND_SERVICE`、`ProtectSystem=strict`、`Restart=always`，并有自己的内存上限。面板仍通过 Xray API 增删客户端而无需重启，只有入站结构变化时才重启内核——且先用 `xray run -test` 检查配置；通过检查却启动失败的配置会被换回上一个可用配置。重启、升级或面板崩溃都不再断开客户端：已验证在通过 VPN 下载 20 MB 的过程中执行 `systemctl restart still-ui`，文件完整到达，Xray 进程未变。默认情况下 Xray 仍和 3x-ui 一样是面板的子进程。内置的 AmneziaWG、MTProto 和 TUIC 仍运行在面板内部，面板重启时会重新连接。
- **自动加载续期后的证书。** Xray 每小时（或每 `ocspStapling` 秒）会自行重新读取 TLS 和 Hysteria 入站的证书文件。面板每分钟通过握手（TLS，Hysteria 用 QUIC）检查每个入站实际提供的证书；如果续期后内核在该时间内没有加载新文件（或入站使用 `oneTimeLoading`），或者正在提供的证书将在一小时内过期，只重启 Xray：使用 `still-xray` 服务时自动重启；Xray 在面板内运行（默认）时，只有在 `/etc/default/still-ui` 中设置 `STILL_CERT_RESTART=on` 才会重启，因为这种重启会断开所有客户端；未设置时标签只显示证书已续期。概览页上的“证书”标签显示每个入站提供的证书及其有效期，并提前警告（提前一周；对于 IP 上的短期证书则提前其有效期的四分之一）。
- **AmneziaWG 转发缓冲区复用。** 经内置 AmneziaWG 的每个 TCP 连接都会新分配两个 32 KiB 复制缓冲区；现在改为从池中获取。仅转发部分：每个连接的分配约 69 KB → 2.9 KB。整个面板在 2 vCPU / 2 GiB 测试服务器上，经 AmneziaWG 每秒 100 个短连接、持续 5 分钟、各 3 次，3x-ui v3.11.0 对比 still-ui：负载下面板平均内存 403 → 176 MiB，峰值 525 → 211 MiB，空闲时相同。方法、限制和原始数据：[bench/awg-relay-churn](bench/awg-relay-churn/README.md)。

### 进行中

- **面板数天内的内存。** AmneziaWG 大量连接下内存增长的原因已找到并修复（见上文）；还需在负载下进行多日测量，确认没有其他增长来源。

完整计划（俄文）见 [docs/ROADMAP.md](docs/ROADMAP.md)。

## 安装

以 root 身份运行：

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)
```

如需安装指定版本，在末尾加上版本标签（标签格式为 `vX.Y.Z-still.N`）：

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v3.11.0-still.4
```

使用已下载的发行版压缩包安装：

```bash
STILL_UI_ARCHIVE=/path/still-ui-linux-amd64.tar.gz bash install.sh
```

安装脚本会随机生成用户名、密码和面板访问路径，并在结束时显示出来，请妥善保存。之后运行 `still-ui` 即可打开管理菜单：启动和停止服务、查看或重置登录信息、修改端口、管理证书等。子命令与 3x-ui 的 `x-ui` 脚本相同：`still-ui start`、`stop`、`restart`、`status`、`log`、`update`、`legacy`、`uninstall` 等。

发行版只提供 **Linux amd64 和 arm64** 的构建。当前版本：面板 **3.11.0-still.4**，内置 **Xray v26.10.21**。

### 文件位置

| 内容 | 位置 |
| --- | --- |
| 服务 | `still-ui`（`systemctl status still-ui`） |
| 程序 | `/usr/local/still-ui` |
| 数据库 | `/etc/still-ui/x-ui.db`（文件名沿用 3x-ui） |
| 日志 | `/var/log/still-ui` |
| 配置 | `/etc/default/still-ui` |
| Xray（可选） | 服务 `still-xray`，配置 `/etc/still-xray`，日志 `/var/log/still-xray` |

### 内存上限

查看当前上限：

```bash
systemctl show still-ui -p MemoryMax
```

如需自定义，在 `/etc/default/still-ui` 中加一行，例如 `STILL_UI_MEMORY_MAX=800M`；写 `STILL_UI_MEMORY_MAX=off` 则取消上限。

### Xray 作为独立服务

开启：在 `/etc/default/still-ui` 中加入 `STILL_XRAY_UNIT=on`，然后运行 `still-ui update`（或安装脚本）。关闭：设为 `STILL_XRAY_UNIT=off` 并再次运行 `still-ui update`。内核的内存上限为 `STILL_XRAY_MEMORY_MAX`（默认内存的一半，256M 到 4G）。需要 TPROXY、TUN 或套接字标记的入站，请用自己的 drop-in 在 `still-xray.service.d` 中加上 `CAP_NET_ADMIN`/`CAP_NET_RAW`。开启时 Xray 会重启一次；之后重启或升级面板不会影响它，因此升级带来的新 Xray 只有在重启 Xray 后才会运行（概览页会提示）。监听 unix 套接字的入站（来自 nginx 的 fallback）必须使用 `/run/still-xray/` 或 `/dev/shm/`，例如 `/dev/shm/xray.sock,0666`：该服务有自己独立的 `/tmp`，其他路径对它都是只读的，因此面板会拒绝其他路径并说明原因。

## 升级、回滚、卸载

升级到最新版本——使用下面的命令，或点击面板中的更新按钮：

```bash
still-ui update
```

回滚到之前的版本——`still-ui legacy` 会询问版本号；也可以带上对应标签运行安装脚本。数据库会保留。

```bash
still-ui legacy
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v<tag>
```

回滚到没有 `still-xray` 服务的版本（例如 `v3.11.0-still.1`）：`still-ui legacy` 会在安装前停止该服务；如果手动运行旧版安装脚本，请先执行 `systemctl disable --now still-xray`。

升级前，尤其是回滚前，请先备份 `/etc/still-ui`：旧版本可能无法读取新版本写入过的数据库。

卸载面板：

```bash
still-ui uninstall
```

> [!WARNING]
> `still-ui uninstall` 会同时删除 `/etc/still-ui` 中的数据库，与 3x-ui 的行为一致。如果以后还可能用到入站和客户端，请先把 `/etc/still-ui/x-ui.db` 复制到安全的地方。

## 从 3x-ui 迁移

如果安装脚本发现 `/etc/x-ui/x-ui.db`，会提议把该数据库复制到 still-ui 并停止 x-ui。在没有终端的环境（如 cloud-init）中，设置 `STILL_UI_IMPORT_3XUI=yes` 即表示同意。面板登录信息、API 访问、入站和客户端都会保留，用户无需更换链接。3x-ui 自身的文件不会被改动。

如果想退回，停止 still-ui，再重新启动 3x-ui 即可：

```bash
systemctl disable --now still-ui
systemctl enable --now x-ui
```

此流程已在 3x-ui v3.11.0 的数据库上验证。更新版本 3x-ui 的数据库，still-ui 可能无法打开。

## 支持本项目

如果 still-ui 对你有帮助，欢迎支持后续开发：

- USDT (TRC20): `TE18ocTtSRa855uCzKFnPFgnSAtJ5Jny2x`
- BTC: `bc1qneuy3u36lrln042uezc7a7ycxd30n5fk29f43e`

通过 DonationAlerts 银行卡支付：https://www.donationalerts.com/r/dv2corn

在 GitHub 上点一颗星也是很大的支持。

## 致谢

- [3x-ui](https://github.com/MHSanaei/3x-ui) —— 感谢 MHSanaei 及所有贡献者，still-ui 建立在他们的工作之上。
- [x-ui](https://github.com/vaxilu/x-ui) —— 感谢一切的起点 vaxilu，以及 [alireza0](https://github.com/alireza0/)。
- [Xray-core](https://github.com/XTLS/Xray-core) —— 感谢 Project X 提供底层引擎。

## 许可证

still-ui 采用 [GNU General Public License v3.0](LICENSE) 许可证发布，与 3x-ui 相同。分发修改版本时，必须以同一许可证公开你的修改。
