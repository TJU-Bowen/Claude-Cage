param(
    [Parameter(Mandatory)][string]$ProductDirectory,
    [Parameter(Mandatory)][string]$BaseHostArchive,
    [Parameter(Mandatory)][string]$OutputArchive
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem

$product = (Resolve-Path -LiteralPath $ProductDirectory).Path
$baseZip = (Resolve-Path -LiteralPath $BaseHostArchive).Path
$output = [IO.Path]::GetFullPath($OutputArchive)
$destination = [IO.Path]::GetDirectoryName($output)
[IO.Directory]::CreateDirectory($destination) | Out-Null
$nonce = [guid]::NewGuid().ToString('N')
$temporaryZip = Join-Path $destination ('.cage-host-' + $nonce + '.tmp')
$launcher = Join-Path $destination ('.cage-start-' + $nonce + '.exe')
$desktop = Join-Path $product 'EXE\Cage.exe'
$controller = Join-Path $product 'EXE\resources\cage.exe'
$lan = Join-Path $product 'Workstation\CageLAN.exe'
$version = (Get-Content -LiteralPath (Join-Path $product 'EXE\version.txt') -Raw).Trim()

function Add-Payload($archive, [string]$name, [IO.Stream]$source) {
    $entry = $archive.CreateEntry($name, [IO.Compression.CompressionLevel]::Optimal)
    $target = $entry.Open()
    try { $source.CopyTo($target) } finally { $target.Dispose() }
}
function Add-ProgramFile($archive, [string]$name, [string]$file) {
    $stream = [IO.File]::OpenRead($file)
    try { Add-Payload $archive $name $stream } finally { $stream.Dispose() }
}

try {
    $compiler = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
    $icon = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..\..\Cage.ico')).Path
    & $compiler /nologo /codepage:65001 /optimize+ /target:winexe /platform:x64 "/out:$launcher" "/win32icon:$icon" /reference:System.Windows.Forms.dll (Join-Path $PSScriptRoot 'StartLanHost.cs')
    if ($LASTEXITCODE -ne 0) { throw 'Failed to build the profile launcher.' }

    $base = [IO.Compression.ZipFile]::OpenRead($baseZip)
    try {
        $prefix = 'Cage Host/'
        $versionEntry = $base.GetEntry($prefix + 'version.txt')
        if (!$versionEntry) { throw 'This is not a Cage Host program archive.' }
        $reader = New-Object IO.StreamReader($versionEntry.Open())
        try { $baseVersion = $reader.ReadToEnd().Trim() } finally { $reader.Dispose() }
        if ($baseVersion -ne $version) { throw 'The packaged Windows runtime has a different base version.' }
        $allowedRoot = @('Cage.exe','Cage.install-id','DEVELOPMENT-BUILD.txt','LICENSE.txt','README.txt','version.txt',
            'resources/cage.exe','resources/mihomo.exe','resources/cloud-linux-amd64.tar','resources/cloud-linux-amd64.tar.sha256','resources/dependencies.json')
        $names = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
        $archive = [IO.Compression.ZipFile]::Open($temporaryZip, [IO.Compression.ZipArchiveMode]::Create)
        try {
            # Use the known program-only base for dependencies. Never enumerate the user's installed data.
            foreach ($entry in $base.Entries) {
                if (!$entry.Name) { continue }
                if (!$entry.FullName.StartsWith($prefix, [StringComparison]::Ordinal)) { throw 'Unexpected top-level entry in the base archive.' }
                $rel = $entry.FullName.Substring($prefix.Length)
                if ($rel -match '[\\:]|^/|/$|//|(^|/)\.{1,2}(/|$)' -or !$names.Add($rel)) { throw ('Invalid program entry ' + $rel) }
                if ($rel -notin $allowedRoot -and $rel -notmatch '^resources/(guest-os|runtime|licenses)/') { throw ('Unexpected program entry ' + $rel) }
                if ($rel -eq 'Cage.exe') { Add-ProgramFile $archive ('EXE/' + $rel) $desktop; continue }
                if ($rel -eq 'resources/cage.exe') { Add-ProgramFile $archive ('EXE/' + $rel) $controller; continue }
                $stream = $entry.Open()
                try { Add-Payload $archive ('EXE/' + $rel) $stream } finally { $stream.Dispose() }
            }
            foreach ($required in @('Cage.exe','resources/cage.exe','resources/runtime/openssh/ssh.exe','resources/runtime/qemu/qemu-system-x86_64.exe')) {
                if (!$names.Contains($required)) { throw ('Missing required program ' + $required) }
            }
            Add-ProgramFile $archive 'Workstation/CageLAN.exe' $lan
            Add-ProgramFile $archive '启动局域网服务端.exe' $launcher
            Add-ProgramFile $archive '使用说明.txt' (Join-Path $PSScriptRoot 'README.zh-CN.txt')
        } finally { $archive.Dispose() }
    } finally { $base.Dispose() }

    if ([IO.File]::Exists($output)) { [IO.File]::Replace($temporaryZip, $output, [Management.Automation.Language.NullString]::Value) }
    else { [IO.File]::Move($temporaryZip, $output) }
    Get-Item -LiteralPath $output
} finally {
    foreach ($file in @($temporaryZip, $launcher)) {
        if ([IO.File]::Exists($file)) { [IO.File]::Delete($file) }
    }
}
