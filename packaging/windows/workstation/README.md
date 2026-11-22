# Cage Windows 工作机

工作台使用 Paseo Desktop。工作机在局域网中发现 Cage，获准后通过加密连接使用原有 Linux 客机中的 Claude Code。会话调用的文件读写和 PowerShell 在选择项目的 Windows 工作机上执行。

## 使用

1. 从 `启动 Cage Host.lnk` 打开 Cage。左侧「工作机连接」位于 SSH 和 Claude Desktop 之间，右侧显示申请和已配对工作机。
2. 双击 `启动 Cage 工作机.lnk`，工作台自动搜索局域网服务器。在目标设备上点「申请连接」，核对两端显示的六位编号，在 Cage 右侧点「批准连接」。
3. 工作台自动打开。新建工作区，选择 Windows 项目目录，使用 `Claude · Cage` 发起会话。以后双击工作机入口即可重连上次使用的服务器。点击工作台左侧「计划」下方的「局域网服务器」，可在右侧搜索、申请连接或打开已配对的服务器。
4. 要移除工作机访问权，在 Cage 的「已配对工作机」中点「撤销授权」。换到另一台 Windows 工作机时，从新的设备身份申请连接。工作机发行件只需程序和适配器；设备身份由程序在首次启动时生成。

服务端在 `Data\LAN-Server` 保存设备授权，工作机在 `Workstation\Data\LAN` 保存自己的身份及每台服务器的工作台会话。局域网发现使用 IPv4 mDNS（UDP 5353），加密接入使用 TCP 17942。其他电脑搜索不到服务器时，可在 Cage 的「工作机连接 → 局域网连接设置」允许这两个本地子网入站端口。原有本机工作台仍可从服务器列表单独打开，原有会话使用 `Workstation\Data\Paseo`。

## 将已有 Windows 宿主升级为局域网服务端

Windows x64 上已经配置好的 0.1.8 Linux 客机可以直接使用程序包。将服务端包解压到新的本地目录，退出旧 Cage 后使用「启动局域网服务端.exe」加载原来的 `config.json`。具体步骤见包内「使用说明.txt」。工作机连接时，在这台宿主的「工作机连接」页批准配对。

构建程序包时，在仓库根目录用 PowerShell 运行以下命令。它从同基础版本的纯程序包读取运行库，加入刚构建的桌面程序、控制程序和局域网服务。

```powershell
.\packaging\windows\workstation\host-bundle\package.ps1 -ProductDirectory .. -BaseHostArchive ..\Release\Cage-Host-0.1.8-windows-x64.zip -OutputArchive ..\Release\Cage-LAN-Host-0.1.8-windows-x64.zip
```

## 会话存储和清理

在 Cage 的「工作机连接」中点击已配对工作机，查看这台设备在客机中的对话记录和占用空间。可逐条删除，也可勾选多条后点「删除所选」。正在运行的会话需结束后再清理。旧版本按机器名保存记录，工作机重连时会用现有本地记录核对归属。

工作机的「历史」列出已归档会话。点击该行的「彻底删除」，或右键选择「彻底删除本机会话」，即可清理当前工作台的记录、本地对话文件和缓存。两端分别确认和清理。服务端记录删除后，要在工作机上继续与 Claude 工作，请新建会话。

## 重建 Windows x64 客户端

| 组件 | 本版使用的源码或发行件 | 在 Cage 总目录中的产物 |
| --- | --- | --- |
| Cage 启动器 | 本仓库 `cmd/cage-workstation`，Go 1.26.10 | `Workstation\CageWorkstation.exe` |
| 发现、配对和加密接入 | 本仓库 `cmd/cage-lan`、`internal/workstationlan` | `Workstation\CageLAN.exe` |
| ACP 适配器 | `AkaraChen/cc-remote` 提交 `063f81524e84b7ad40a203d4c7da6f92fcfd2e41`，应用同目录的 `cc-remote.patch`，Bun 1.4.5 | `Workstation\bin\cc-remote.exe` |
| 图形工作台 | `getpaseo/paseo` 的 Windows x64 发行版 `v0.17.2` | `Workstation\Paseo\Paseo.exe` 及完整发行目录 |
| 工作台局域网功能 | 本目录 `paseo-lan` | `Workstation\PaseoLAN` |

在本仓库根目录运行 `go build -trimpath -ldflags '-s -w -H=windowsgui' -o ..\Workstation\CageWorkstation.exe ./cmd/cage-workstation`。在上述 cc-remote 提交的干净检出中，用 `git apply` 应用补丁，运行 `bun install --frozen-lockfile` 和 `bun run build:windows-x64`，将 `dist\cc-remote-windows-x64.exe` 复制到表中指定位置。

在本仓库根目录运行 `go build -trimpath -ldflags '-s -w -H=windowsgui' -o ..\Workstation\CageLAN.exe ./cmd/cage-lan` 和 `node packaging/windows/workstation/paseo-lan/build.mjs ..\Workstation`。服务端界面由 `npm --prefix ui run build` 和随后的 Windows 桌面应用发布步骤生成。构建脚本在同一磁盘上用硬链接复用工作台发行件的公共文件。

Paseo 原始安装文件 `Paseo-Setup-0.17.2-x64.exe` 的 SHA-256 为 `AFE6F5F78EE10A5BF6F1E520A380C611E1A25E7A78E3D90C5DEB59D97F0CB7BA`。首启时启动器从 `cmd/cage-workstation/paseo.json` 生成客户端配置，后续启动读取已有配置。

## macOS 工作机移植入口

Windows Cage 继续运行已登录的 Claude Code。Mac 工作台负责显示会话，并在用户选择的 Mac 项目目录中读写文件和执行 shell 命令。

- 共享的发现、配对和隧道代码位于 [internal/workstationlan](../../../internal/workstationlan)。它已通过 Apple Silicon 和 Intel 两种 macOS 目标的交叉编译。
- 工作机执行适配器使用上表中固定的 cc-remote 版本和[本仓库的补丁](cc-remote.patch)。这份源码已生成两种架构的 Mach-O 可执行文件。Paseo v0.17.2 也有两种架构的 macOS 打包配置。
- [工作台启动器](../../../cmd/cage-workstation)和[局域网程序入口](../../../cmd/cage-lan)当前只有 Windows 实现。移植时需要补齐 Darwin 客户端入口、Paseo.app 的资源与启动路径、工作机数据路径，以及已归档会话清理时使用的本地 Paseo 程序路径。侧栏和历史页的现有实现见 [paseo-lan](paseo-lan)。

在 Mac 真机上，从左侧「局域网服务器」发现 Windows Cage，在 Windows Cage 内批准申请，重开 Mac 工作台后自动重连。随后在 Mac 临时项目里让 Claude 写入一个文件，并运行 `pwd` 和 `uname -s` 核对执行位置。完整的 Mac 工作台仍待实现和真机验收。

早期手动 SSH 原型的[适配器启动脚本](../../macos/workstation/cage-claude)与[交互式接入说明](../../macos/workstation/manual-ssh.zh-CN.html)保存在仓库中。局域网工作台使用 Cage 内批准的配对流程。
