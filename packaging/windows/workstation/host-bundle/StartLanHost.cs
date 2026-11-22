using System;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Text;
using System.Windows.Forms;

internal static class StartLanHost
{
    private static readonly string Bundle = AppDomain.CurrentDomain.BaseDirectory;
    private static readonly string SavedChoice = Path.Combine(Bundle, "server-profile.path");

    private static bool HasLinuxGuest(string file)
    {
        if (String.IsNullOrWhiteSpace(file) || !File.Exists(file) ||
            !String.Equals(Path.GetFileName(file), "config.json", StringComparison.OrdinalIgnoreCase))
            return false;
        var lima = Path.Combine(Path.GetDirectoryName(Path.GetFullPath(file)), "state", "lima");
        return Directory.Exists(lima) && Directory.EnumerateDirectories(lima).Any(dir => {
            var name = Path.GetFileName(dir);
            return !name.StartsWith("_", StringComparison.Ordinal) &&
                !String.Equals(name, "base", StringComparison.OrdinalIgnoreCase) &&
                File.Exists(Path.Combine(dir, "lima.yaml"));
        });
    }

    private static string SelectProfile(string argument, bool checkOnly, out bool remember)
    {
        remember = false;
        if (!String.IsNullOrWhiteSpace(argument)) return Path.GetFullPath(argument);
        var saved = File.Exists(SavedChoice) ? File.ReadAllText(SavedChoice).Trim() : null;
        if (HasLinuxGuest(saved)) return saved;
        var current = Environment.GetEnvironmentVariable("CLAUDE_CAGE_CONFIG");
        if (HasLinuxGuest(current)) return Path.GetFullPath(current);

        var usual = new[] {
            Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.UserProfile), ".claude-cage", "config.json"),
            Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory), "ClaudeCage", "Data", "Profile", "config.json"),
        };
        var known = usual.Where(HasLinuxGuest).Distinct(StringComparer.OrdinalIgnoreCase).ToList();
        if (known.Count == 1 && String.IsNullOrEmpty(saved) && String.IsNullOrEmpty(current)) return known[0];
        if (checkOnly) throw new IOException("请指定这台电脑现有 Cage 的 config.json。");
        using (var choose = new OpenFileDialog()) {
            choose.Title = "选择这台电脑已经配置好的 Cage Host 的 config.json";
            choose.Filter = "Cage 配置文件 (config.json)|config.json";
            choose.FileName = "config.json";
            choose.CheckFileExists = true;
            if (known.Count > 0) choose.InitialDirectory = Path.GetDirectoryName(known[0]);
            if (choose.ShowDialog() != DialogResult.OK) return null;
            remember = true;
            return Path.GetFullPath(choose.FileName);
        }
    }

    private static void SaveChoice(string config)
    {
        var temporary = Path.Combine(Bundle, ".profile-" + Guid.NewGuid().ToString("N") + ".tmp");
        try {
            File.WriteAllText(temporary, config, new UTF8Encoding(false));
            if (File.Exists(SavedChoice)) File.Replace(temporary, SavedChoice, null);
            else File.Move(temporary, SavedChoice);
        } catch (UnauthorizedAccessException) {
            // A read-only bundle can still launch an explicitly selected profile.
        } catch (IOException) {
            // A simultaneous launch may already have saved its own choice.
        } finally {
            try { if (File.Exists(temporary)) File.Delete(temporary); } catch (IOException) { } catch (UnauthorizedAccessException) { }
        }
    }

    [STAThread]
    private static int Main(string[] args)
    {
        bool checkOnly = args.Length > 0 && args[0] == "--check";
        try {
            var options = checkOnly ? args.Skip(1).ToArray() : args;
            if (options.Length > 1) throw new ArgumentException("请传入一个现有 config.json 的完整路径。");
            var program = Path.Combine(Bundle, "EXE", "Cage.exe");
            var required = new[] { program, Path.Combine(Bundle, "Workstation", "CageLAN.exe"),
                Path.Combine(Bundle, "EXE", "resources", "cage.exe"),
                Path.Combine(Bundle, "EXE", "resources", "runtime", "openssh", "ssh.exe") };
            if (!required.All(File.Exists)) throw new IOException("请先将完整升级包解压到同一个本地文件夹。");
            bool remember;
            var config = SelectProfile(options.FirstOrDefault(), checkOnly, out remember);
            if (config == null) return 1;
            if (!HasLinuxGuest(config)) throw new IOException("所选配置旁未找到已经部署的 Linux 客机。请选旧 Cage 当前使用的 config.json。");
            if (checkOnly) return 0;

            var start = new ProcessStartInfo(program, "--workstation-connections");
            start.UseShellExecute = false;
            start.WorkingDirectory = Path.GetDirectoryName(program);
            start.EnvironmentVariables["CLAUDE_CAGE_CONFIG"] = config;
            using (Process.Start(start)) { }
            if (remember) SaveChoice(config);
            return 0;
        } catch (Exception error) {
            if (!checkOnly) MessageBox.Show(error.Message, "启动 Cage 局域网服务端", MessageBoxButtons.OK, MessageBoxIcon.Error);
            return 2;
        }
    }
}
