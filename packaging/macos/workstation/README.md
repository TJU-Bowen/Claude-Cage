# Cage macOS 工作机

Windows Cage 继续运行已登录的 Claude Code。首次打开 Mac 工作台会显示「局域网服务器」。申请连接后，在 Windows Cage 的「工作机连接」核对六位编号并批准。以后打开 Mac 工作台会尝试重连上次选择的服务器。工作台左侧的「局域网服务器」可再次打开服务器列表。

如看到 Paseo 的新手引导，可以点击「跳过设置」。在 Mac 工作台打开自己的项目并选择 `Claude · Cage`。项目的文件工具和 shell 由 Mac 上的适配器运行。配对身份及每台服务器的工作台资料保存在 `~/Library/Application Support/Claude Cage Workstation/LAN`。

## 构建

在对应架构的 Mac 上准备 Go、Node.js 和 Xcode 命令行工具。取 [Paseo v0.17.2](https://github.com/getpaseo/paseo/releases/tag/v0.17.2) 的对应架构发行件。按 [Windows 工作机的版本表](../../windows/workstation/README.md#重建-windows-x64-客户端) 检出同一个 cc-remote 提交，应用 Cage 补丁，并用 Bun 1.4.5 构建。

```sh
# 在已应用补丁的 cc-remote 检出中
bun install --frozen-lockfile
bun build src/main.ts --compile --minify --target="bun-darwin-$(node -p process.arch)" --outfile dist/cc-remote

# 回到 Claude Cage 仓库根目录
bash packaging/macos/workstation/build-app.sh /path/to/Paseo.app /path/to/cc-remote "_output/Cage Workstation.app"
```

打包脚本默认生成本地 ad-hoc 签名的应用，可用 `CAGE_SIGNING_IDENTITY` 指定代码签名证书。请保持 Windows 服务端的局域网发现已启用。macOS 提示时允许应用访问本地网络，也可以在「系统设置 → 隐私与安全性 → 本地网络」检查权限。
