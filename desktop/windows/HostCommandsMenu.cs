using System.Diagnostics;
using System.Security.AccessControl;
using System.Security.Principal;
using System.Text.Json;

namespace Cage.Windows;

// Native-only consent. No WebView message or controller API writes this marker.
internal sealed class HostCommandsMenu(string profile, string backendExecutable, Form owner) : ToolStripMenuItem
{
    private string Marker => Path.Combine(profile, "host-commands-v1.enabled");

    private string? BridgeState()
    {
        try
        {
            using var status = JsonDocument.Parse(File.ReadAllBytes(Path.Combine(profile, "host-commands-v1.status.json")));
            using var control = JsonDocument.Parse(File.ReadAllBytes(Path.Combine(profile, "control.json")));
            if (!status.RootElement.TryGetProperty("pid", out var pidElement) || !pidElement.TryGetInt32(out var pid) || pid <= 0 ||
                !control.RootElement.TryGetProperty("pid", out var controlPid) || !controlPid.TryGetInt32(out var controller) || controller != pid)
                return null;
            using var process = Process.GetProcessById(pid);
            if (process.HasExited || !string.Equals(process.MainModule?.FileName, backendExecutable, StringComparison.OrdinalIgnoreCase))
                return null;
            return status.RootElement.TryGetProperty("state", out var state) && state.ValueKind == JsonValueKind.String
                ? state.GetString() : null;
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException or ArgumentException or
                                          InvalidOperationException or JsonException or System.ComponentModel.Win32Exception) { return null; }
    }

    public void RefreshStatus()
    {
        var state = BridgeState();
        var granted = File.Exists(Marker);
        Enabled = granted || state is "disabled" or "waiting" or "connected" or "retrying";
        Text = granted ? state switch {
            "connected" => "关闭 Windows 宿主命令（已连接）",
            "retrying" => "关闭 Windows 宿主命令（重连中）",
            _ => "关闭 Windows 宿主命令（等待本地客机）",
        } : Enabled ? "允许本地 Linux 客机执行任意 Windows 命令" : "Windows 宿主命令（需要本地 Linux 环境）";
        ToolTipText = "宿主命令以运行 Cage 的 Windows 用户权限执行，使用本机文件和网络。";
    }

    protected override void OnClick(EventArgs e)
    {
        try
        {
            if (File.Exists(Marker)) File.Delete(Marker);
            else if (BridgeState() is "disabled" or "waiting" or "connected" or "retrying")
            {
                var temporary = Path.Combine(profile, $".host-commands-{Guid.NewGuid():N}.tmp");
                try
                {
                    using var identity = WindowsIdentity.GetCurrent();
                    var user = identity.User ?? throw new UnauthorizedAccessException("无法识别当前 Windows 用户");
                    var permissions = new FileSecurity();
                    permissions.SetOwner(user);
                    permissions.SetAccessRuleProtection(true, false);
                    permissions.AddAccessRule(new FileSystemAccessRule(user, FileSystemRights.FullControl, AccessControlType.Allow));
                    var system = new SecurityIdentifier(WellKnownSidType.LocalSystemSid, null);
                    permissions.AddAccessRule(new FileSystemAccessRule(system, FileSystemRights.FullControl, AccessControlType.Allow));
                    using (var file = new FileInfo(temporary).Create(FileMode.CreateNew, FileSystemRights.FullControl,
                               FileShare.None, 4096, FileOptions.None, permissions))
                        file.Write("cage-host-commands-v1\n"u8);
                    File.Move(temporary, Marker);
                }
                finally { File.Delete(temporary); }
            }
        }
        catch (Exception error) when (error is IOException or UnauthorizedAccessException)
        {
            MessageBox.Show(owner, "无法更新 Windows 宿主命令授权。\n" + error.Message, "Cage", MessageBoxButtons.OK, MessageBoxIcon.Error);
        }
        RefreshStatus();
        base.OnClick(e);
    }
}
