Claude Cage 局域网服务端升级

适用于 Windows x64 和已配置好的 Cage Host 0.1.8 Linux 客机。本包提供桌面管理程序、后台控制程序、匹配的运行库和局域网服务进程。

在第二台电脑上

1. 在旧 Cage 中保存正在做的工作。记下它正在使用的 config.json 位置。使用旧 Cage 的停止环境和托盘退出功能正常结束旧程序。
2. 把整个 ZIP 解压到一个新的本地目录，例如 D:\ClaudeCage-LAN。保留原来的 Cage 安装和数据目录。
3. 用原来的 Windows 用户双击「启动局域网服务端.exe」。它优先读取此前手动选定的配置，其次读取 CLAUDE_CAGE_CONFIG，再查找默认和桌面文件夹中的已有配置。出现选择框时，选择这台电脑原先使用的 config.json。
   安装版通常使用 %USERPROFILE%\.claude-cage\config.json。按 ClaudeCage 文件夹部署的版本通常使用原目录下的 Data\Profile\config.json。也可把确切配置文件路径作为启动器的参数传入。
4. 在 Cage 中解锁并启动原环境，确认熟悉的项目、代理和客机中的 Claude Code 都正常。「工作机连接」页应显示可在局域网中发现。若另一台工作机搜不到，在同一个可互访的局域网上，通过该页的「局域网连接设置」允许此服务的本地子网入站。发现使用 UDP 5353，连接使用 TCP 17942。
5. 在工作机客户端左侧打开「局域网服务器」，按第二台电脑的机器名或局域网地址选择它，点击「申请连接」。在第二台电脑的 Cage → 工作机连接核对配对编号，点击「批准连接」。随后在工作机中新建一条短会话，验证实际使用的是第二台电脑客机中的 Claude Code。若提示未登录，到该客机终端运行 claude auth status 查看 CLI 登录状态。

只读核对

启动器接受两个参数，例如用 PowerShell 在解压目录运行以下命令。退出码 0 表示已找到服务端程序及既有 Linux 客机配置，2 表示核对失败。本步骤不会启动虚拟机。

    $config = 'D:\原来的ClaudeCage\Data\Profile\config.json'
    $launcher = Join-Path (Get-Location) '启动局域网服务端.exe'
    $check = Start-Process -FilePath $launcher -ArgumentList ('--check "' + $config + '"') -Wait -PassThru
    $check.ExitCode

服务端程序从所选配置加载原机的虚拟磁盘、账户、代理和项目。首次启用时会在这台电脑上创建局域网服务身份，已存在的服务身份会继续使用。本包只分发程序，工作机的会话和本机的虚拟盘均不参与分发。若想再使用旧程序，先正常退出新的 Cage，然后从原来的启动入口打开原环境。
