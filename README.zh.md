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

- **部分修改客户端不再把客户端改坏。** 在 3x-ui v3.11.0 中，通过 API（`/panel/api/clients/update/:email`）更新客户端时如果只提交部分字段，客户端会被禁用，请求中没有的 `limitIp`、`tgId`、`comment` 等字段也会被清零。在 still-ui 中，未提交的字段一律保持原值——密钥和客户端设置（包括 AmneziaWG 的 `preSharedKey`）都不会丢失。显式提交 0 或空值仍然会清空对应字段。
- **REALITY 默认指纹改为 `ios`。** 它的 ClientHello 约 517 字节，可以完整放进一个 TCP 分段。较大的指纹（`chrome`、`firefox`）会把 ClientHello 拆成两个包；在会丢掉第二个包的移动运营商网络上，连接会直接断在握手阶段。用 `ios` 就不存在这个问题。每个入站仍然可以自行选择其他指纹。
- **安装脚本为面板设置内存上限。** 服务会获得 systemd 限制：`MemoryMax` 为内存的 35%（最少 256 MiB，最多 1 GiB），`MemoryHigh` 为其 85%，并且禁止面板使用 swap。面板内存失控时由 systemd 重启它，而不是把整台服务器拖进 swap 直至卡死。如果服务器没有 swap 且内存不超过 2 GiB，安装脚本会提议为系统其余部分创建一个 2 GiB 的 `/swapfile`。

### 进行中

- **修复面板的内存泄漏** —— 不只是设上限，而是找到并消除根源。
- **Xray 作为独立服务运行。** 重启或升级面板时不再断开已连接的用户：Xray 会独立运行。
- **自动加载续期后的证书。** 证书续期后，入站自动使用新证书，无需手动重启。

完整计划（俄文）见 [docs/ROADMAP.md](docs/ROADMAP.md)。

## 安装

以 root 身份运行：

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)
```

如需安装指定版本，在末尾加上版本标签（标签格式为 `vX.Y.Z-still.N`）：

```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) v3.11.0-still.1
```

使用已下载的发行版压缩包安装：

```bash
STILL_UI_ARCHIVE=/path/still-ui-linux-amd64.tar.gz bash install.sh
```

安装脚本会随机生成用户名、密码和面板访问路径，并在结束时显示出来，请妥善保存。之后运行 `still-ui` 即可打开管理菜单：启动和停止服务、查看或重置登录信息、修改端口、管理证书等。子命令与 3x-ui 的 `x-ui` 脚本相同：`still-ui start`、`stop`、`restart`、`status`、`log`、`update`、`legacy`、`uninstall` 等。

发行版只提供 **Linux amd64 和 arm64** 的构建。当前版本：面板 **3.11.0-still.1**，内置 **Xray v26.10.21**。

### 文件位置

| 内容 | 位置 |
| --- | --- |
| 服务 | `still-ui`（`systemctl status still-ui`） |
| 程序 | `/usr/local/still-ui` |
| 数据库 | `/etc/still-ui/x-ui.db`（文件名沿用 3x-ui） |
| 日志 | `/var/log/still-ui` |
| 配置 | `/etc/default/still-ui` |

### 内存上限

查看当前上限：

```bash
systemctl show still-ui -p MemoryMax
```

如需自定义，在 `/etc/default/still-ui` 中加一行，例如 `STILL_UI_MEMORY_MAX=800M`；写 `STILL_UI_MEMORY_MAX=off` 则取消上限。

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

- USDT (TRC20): `<USDT_TRC20_ADDRESS>`
- USDT (TON): `<USDT_TON_ADDRESS>`
- BTC: `<BTC_ADDRESS>`
- ETH: `<ETH_ADDRESS>`

通过 DonationAlerts 银行卡支付：<DONATIONALERTS_URL>

在 GitHub 上点一颗星也是很大的支持。

## 致谢

- [3x-ui](https://github.com/MHSanaei/3x-ui) —— 感谢 MHSanaei 及所有贡献者，still-ui 建立在他们的工作之上。
- [x-ui](https://github.com/vaxilu/x-ui) —— 感谢一切的起点 vaxilu，以及 [alireza0](https://github.com/alireza0/)。
- [Xray-core](https://github.com/XTLS/Xray-core) —— 感谢 Project X 提供底层引擎。

## 许可证

still-ui 采用 [GNU General Public License v3.0](LICENSE) 许可证发布，与 3x-ui 相同。分发修改版本时，必须以同一许可证公开你的修改。
