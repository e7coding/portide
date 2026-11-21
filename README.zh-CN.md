# Portide（摆渡）

在你自己的设备之间互传文件和文字：手机到电脑、电脑到电脑，在家里或者跨网络都行。端到端加密，不用注册账号，也没有云端存储。

[English](README.md)

> **现状：0.5.1。** Windows、macOS 和 Linux 的安装包在 [Releases 页面](https://github.com/e7coding/portide/releases/latest)。Mac 版经过苹果签名和公证，Windows 安装包暂未代码签名。还没有官方中继，跨网络连接需要自己架中继。iOS 应用以后推出，Android 在路线图上。

## 组成

- `cmd/portide`：命令行（配对、收发、离线队列、历史、移除设备、中继）。实测说明见 `docs/测试说明.md`。
- `cmd/portide-relay`：自建中继服务器，部署见 `docs/中继部署.md`；服务端实现在 `core/relayserver`。
- `desktop/`：桌面端（Wails v3 + React），独立的 Go 模块，说明见 `docs/桌面端.md`。
- `mobile/`：移动端经 gomobile 调用的 Go 入口（独立的 Go 模块）。iOS 应用本身不开源。
- `app/`：界面调用的服务层，桌面端和 iOS 共用。
- `core/`：协议、存储和传输的核心实现，命令行、桌面端和 iOS 共用。
- `spikes/`：M0 技术验证的一次性代码，结论见 `docs/m0-report.md`。

持续集成：`.github/workflows/ci.yml` 在推送 main 和合并请求时跑全部测试和打包检查；发版时改 `core/version/VERSION` 并推送同名标签（如 `v0.5.0`），`release.yml` 打出全部安装包、建草稿 Release、把中继镜像推到 ghcr.io（仓库配好 `release.yml` 开头列出的签名密钥时，macOS 安装包会签名并经苹果公证；Windows 安装包暂未代码签名）。`.gitlab-ci.yml` 是私有 GitLab 上的同等配置，不含 iOS 和 macOS。

安全设计的概要见英文 [README](README.md#how-the-security-works)，完整说明（威胁模型、已知限制）见 [docs/en/protocol.md](docs/en/protocol.md)；发现安全漏洞请按 [SECURITY.md](SECURITY.md) 私下报告。

## 洁净室声明

Portide 参考了 LocalSend（Apache-2.0）的公开设计思路，但**没有复制它的任何代码**，协议也是独立设计的。

## 许可证

- 除下面一项外，全部代码使用 [Apache-2.0](LICENSE)。
- 中继服务端 `core/relayserver/`、`cmd/portide-relay/` 和 `deploy/relay/` 使用 [AGPL-3.0](core/relayserver/LICENSE)。客户端代码不得导入 `core/relayserver`；两端都要用的协议部分放在 `core/relay`（Apache-2.0）。

"Portide"名称和图标不在上述许可证的授权范围内。参与贡献前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 并签署 [CLA](CLA.md)。
