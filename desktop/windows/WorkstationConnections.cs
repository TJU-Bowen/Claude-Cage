using System.Diagnostics;
using System.Net;
using System.Net.Http.Headers;
using System.Net.Http.Json;
using System.Text.Json;
using Microsoft.Web.WebView2.Core;

namespace Cage.Windows;

internal static class WorkstationConnections
{
    private static string Helper => Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "..", "Workstation", "CageLAN.exe"));
    public static bool Available => !Backend.ClientOnly && File.Exists(Helper);
    private static string ControlRecord => Path.Combine(Directory.GetParent(Backend.Root)!.FullName, "LAN-Server", "control.json");
    private static readonly HttpClient Http = new(new HttpClientHandler { UseProxy=false, AllowAutoRedirect=false }) { Timeout=TimeSpan.FromSeconds(65) };
    public static void Start(){
        var info=new ProcessStartInfo(Helper){UseShellExecute=false,CreateNoWindow=true};
        info.ArgumentList.Add("host");info.ArgumentList.Add("--headless");
        info.Environment["CLAUDE_CAGE_CONFIG"]=Path.Combine(Backend.Root,"config.json");
        Process.Start(info)?.Dispose();
    }
    public static async Task<JsonElement> Invoke(JsonElement args,CancellationToken token){
        if(!Available)throw new IOException("此 Cage 安装没有工作机服务");
        var route=args.Text("operation");
        if(route is not ("devices" or "approve" or "deny" or "revoke" or "firewall" or "device-sessions" or "delete-device-sessions"))throw new InvalidDataException("不支持此操作");
        using var data=JsonDocument.Parse(await File.ReadAllTextAsync(ControlRecord,token));
        if(!IPEndPoint.TryParse(data.RootElement.Text("address"),out var end)||!end.Address.Equals(IPAddress.Loopback))
            throw new IOException("工作机服务入口无效");
        var origin="http://"+end;
        using var request=new HttpRequestMessage(route=="devices"?HttpMethod.Get:HttpMethod.Post,origin+"/api/"+route);
        request.Headers.Authorization=new AuthenticationHeaderValue("Bearer",data.RootElement.GetProperty("token").GetString());
        if(route!="devices"){
            request.Headers.Add("Origin",origin);
            string[] ids=args.TryGetProperty("ids",out var selected) ? selected.Deserialize<string[]>() ?? [] : [];
            request.Content=JsonContent.Create(new{id=args.Text("device"),ticket=args.Text("ticket"),ids});
        }
        using var response=await Http.SendAsync(request,token);
        using var result=JsonDocument.Parse(await response.Content.ReadAsStringAsync(token));
        if(!response.IsSuccessStatusCode)throw new IOException(result.RootElement.Text("error"));
        return result.RootElement.Clone();
    }

    // The desktop entry can stay alive across a UI upgrade. Use this release's compiled React surface
    // for static workbench assets only, retaining the existing controller's authenticated APIs and sockets.
    public static void InstallWorkbenchAssets(CoreWebView2 core,Func<int> port){
        var asm=typeof(WorkstationConnections).Assembly;
        var names=asm.GetManifestResourceNames().Where(n=>n.StartsWith("Cage.Workbench/",StringComparison.Ordinal))
            .ToDictionary(n=>"/"+n["Cage.Workbench/".Length..],n=>n,StringComparer.Ordinal);
        if(!names.ContainsKey("/index.html"))return;
        core.AddWebResourceRequestedFilter("http://localhost:*/*",CoreWebView2WebResourceContext.All);
        core.WebResourceRequested+=(_,e)=>{
            var raw=e.Request.Uri;
            if(e.Request.Method!="GET"||!UpdatePolicy.IsWorkbench(raw,port()))return;
            var uri=new Uri(raw);
            var file=uri.AbsolutePath=="/"?"/index.html":uri.AbsolutePath;
            if(!names.TryGetValue(file,out var name))return;
            var stream=asm.GetManifestResourceStream(name);if(stream==null)return;
            var type=Path.GetExtension(file) switch {".html"=>"text/html; charset=utf-8",".js"=>"text/javascript; charset=utf-8",".css"=>"text/css; charset=utf-8",".svg"=>"image/svg+xml",".png"=>"image/png",".woff2"=>"font/woff2",_=>"application/octet-stream"};
            var csp="default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws://"+uri.Authority+"; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; object-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'";
            e.Response=core.Environment.CreateWebResourceResponse(stream,200,"OK","Content-Type: "+type+"\r\nCache-Control: no-store\r\nX-Content-Type-Options: nosniff\r\nReferrer-Policy: no-referrer\r\nX-Frame-Options: DENY\r\nContent-Security-Policy: "+csp);
        };
    }
}
