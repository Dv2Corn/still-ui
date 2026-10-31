[English](README.md) | [Русский](README.ru.md) | [فارسی](README.fa.md) | **中文**

# still-ui

<p>
  <a href="https://www.gnu.org/licenses/gpl-3.0.en.html"><img src="https://img.shields.io/badge/license-GPL%20V3-blue.svg?longCache=true" alt="许可证：GPL-3.0"></a>
  <a href="https://github.com/MHSanaei/3x-ui"><img src="https://img.shields.io/badge/based%20on-3x--ui%20v3.11.0-555" alt="基于 3x-ui v3.11.0"></a>
</p>

**still-ui** 是 MHSanaei 开发的 [3x-ui](https://github.com/MHSanaei/3x-ui) 面板的一个分支（fork），用于管理 [Xray-core](https://github.com/XTLS/Xray-core)。它以 3x-ui **v3.11.0** 为基础，保留了原面板的全部能力：相同的协议、相同的界面、相同的数据库。

不同之处在于关注点。当面板在一台小服务器上连续运行几个月、用户大多使用移动网络时，一些问题会逐渐暴露出来：密钥莫名丢失、握手在丢包严重的线路上失败、面板进程一点点吃光服务器内存。still-ui 专门解决这些问题。

名字本身就说明了目标：服务器配置一次，之后就不用再操心。

> [!IMPORTANT]
> still-ui 是一个独立分支，与 3x-ui 的作者没有任何关联，也未获得其背书。still-ui 的问题请在本仓库反馈，不要提交到 3x-ui。

## 有什么不同

### 已完成

- **编辑客户端不会再清除 AmneziaWG 密钥。** 在 3x-ui 中，如果更新 AmneziaWG 客户端时没有带上它的 `preSharedKey`，密钥会被悄悄清空，客户端随之无法连接。still-ui 会保留已保存的密钥，除非你主动修改。
- **REALITY 默认指纹改为 `ios`。** 它的 ClientHello 约 517 字节，可以完整放进一个 TCP 分段。较大的指纹（`chrome`、`firefox`）会把 ClientHello 拆成两个包；在会丢掉第二个包的移动运营商网络上，连接会直接断在握手阶段。用 `ios` 就不存在这个问题。每个入站仍然可以自行选择其他指纹。
- **安装脚本为面板设置内存上限。** 服务会获得 systemd 的 `MemoryMax` 限制：面板内存失控时由 systemd 重启它，而不是把整台服务器拖进 swap 直至卡死。

### 进行中

- **修复面板的内存泄漏** —— 不只是设上限，而是找到并消除根源。
- **Xray 作为独立服务运行。** 重启或升级面板时不再断开已连接的用户：Xray 会独立运行。
- **自动加载续期后的证书。** 证书续期后，入站自动使用新证书，无需手动重启。

完整计划（俄文）见 [docs/ROADMAP.md](docs/ROADMAP.md)。

## 安装

在一台全新的服务器上以 root 身份运行：

<!-- TODO: сверить с установщиком -->
```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh)
```

如需安装指定版本，在末尾加上版本标签：

<!-- TODO: сверить с установщиком -->
```bash
bash <(curl -Ls https://raw.githubusercontent.com/Dv2Corn/still-ui/main/install.sh) <version>
```

安装脚本会随机生成用户名、密码和面板访问路径，并在结束时显示出来，请妥善保存。之后运行 `still-ui` 即可打开管理菜单：启动和停止服务、查看或重置登录信息、修改端口、管理证书等。

<!-- TODO: сверить с установщиком -->
```bash
still-ui
```

支持的操作系统和 CPU 架构与 3x-ui v3.11.0 相同。

## 升级、回滚、卸载

<!-- TODO: сверить с установщиком -->
```bash
still-ui update      # 升级到最新版本
```

<!-- TODO: сверить с установщиком -->
```bash
still-ui legacy      # 安装指定的旧版本（会询问版本号）
```

<!-- TODO: сверить с установщиком -->
```bash
still-ui uninstall   # 卸载面板及其服务
```

其他常用命令：

<!-- TODO: сверить с установщиком -->
```bash
still-ui status      # 服务状态
still-ui restart     # 重启面板
still-ui log         # 查看日志
```

服务名称为 `still-ui`：

<!-- TODO: сверить с установщиком -->
```bash
systemctl status still-ui
systemctl show still-ui -p MemoryMax   # 安装脚本设置的内存上限
```

每次升级前，尤其是回滚前，请先备份数据库：旧版本可能无法读取新版本写入过的数据库。

## 从 3x-ui 迁移

still-ui 的数据库格式与 3x-ui v3.11.0 相同，入站、客户端和设置都可以原样迁移，用户无需更换链接。

<!-- TODO: сверить с установщиком -->
1. 备份 3x-ui 数据库：`cp /etc/x-ui/x-ui.db /root/x-ui.db.backup`
2. 停止 3x-ui 并关闭开机自启：`systemctl disable --now x-ui`
3. 用上面的命令安装 still-ui。
4. 放回数据库：`cp /root/x-ui.db.backup /etc/still-ui/still-ui.db`，然后执行 `systemctl restart still-ui`。
5. 用原来 3x-ui 的用户名和密码登录，检查入站是否正常。

如果想退回，停止 still-ui（`systemctl disable --now still-ui`），再重新启动 3x-ui（`systemctl enable --now x-ui`）即可，它的数据库没有被改动。

如果你的 3x-ui 版本低于 v3.11.0，请先升级到 v3.11.0 再迁移。

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
