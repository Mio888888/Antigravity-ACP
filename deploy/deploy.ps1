# ==============================================================================
# Antigravity ACP Gateway — Windows PowerShell Deployment Script
# Supports: Windows 10/11 & Windows Server (x64 / ARM64) + Docker Desktop
# Supports Automatic ACP Backend Download & Installation (fail-closed hash verification):
#   - cli (default) : Antigravity CLI (agy.exe acp), SHA-512 from the official manifest
#   - zed / zen     : Zed/Zen standalone ACP backend package (agy_acp_server.exe), pinned SHA-256 (-ZedSha256 / ZED_ACP_SHA256)
#   - auto          : Auto-discover CLI (agy.exe) or Zed/Zen ACP package, auto-installing CLI if missing
#
# The admin password is never written to config\config.yaml; it is passed as AG_ADMIN_PASSWORD
# and only applies until it is changed in the Web Console.
# ==============================================================================
param(
    [ValidateSet("install", "install-acp", "start", "stop", "restart", "status", "build")]
    [string]$Action = "install",

    [ValidateSet("local", "service", "docker")]
    [string]$Mode = "local",

    # Default: keep acp_pool.backend_type from config.yaml
    [ValidateSet("cli", "zed", "zen", "auto")]
    [string]$Backend = "cli",

    [string]$AcpBin = "",
    [switch]$NoDownload,

    # Default: AG_PORT, else server.port from config.yaml (docker: 8080)
    [int]$Port = 0,

    # Discouraged: visible in history / process list. Prefer -AdminPasswordStdin.
    [string]$AdminPassword = "",
    [switch]$AdminPasswordStdin,

    [string]$ZedSha256 = $env:ZED_ACP_SHA256,
    [string]$ZedVersion = $(if ($env:ZED_ACP_VERSION) { $env:ZED_ACP_VERSION } else { "1.4.5" }),

    # Allow installs when no expected hash is available. A hash mismatch is always fatal.
    [switch]$InsecureSkipVerify,

    [ValidateSet("zh", "en")]
    [string]$Lang = "zh"
)

$ErrorActionPreference = "Stop"
$RootDir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $RootDir

$CfgPath = "$RootDir\config\config.yaml"
$GatewayExe = "$RootDir\bin\antigravity-gateway.exe"
$ComposeFile = "$RootDir\deploy\docker-compose.yml"
$DockerEnvFile = "$RootDir\deploy\gateway.env"
$TaskName = "AntigravityACPGateway"

function Write-Bilingual([string]$Zh, [string]$En) {
    if ($Lang -eq "en") {
        Write-Host $En -ForegroundColor Cyan
    } else {
        Write-Host $Zh -ForegroundColor Cyan
    }
}

function Write-BilingualWarning([string]$Zh, [string]$En) {
    if ($Lang -eq "en") { Write-Warning $En } else { Write-Warning $Zh }
}

function Fail([string]$Zh, [string]$En) {
    if ($Lang -eq "en") { throw $En } else { throw $Zh }
}

function Read-ConfigValue([string]$Key) {
    if (-not (Test-Path $CfgPath)) { return "" }
    foreach ($line in Get-Content -Path $CfgPath -Encoding UTF8) {
        if ($line -match "^\s*$Key\s*:\s*(.*)$") {
            $v = ($Matches[1] -replace '\s+#.*$', '').Trim()
            if ($v.Length -ge 2 -and (($v.StartsWith('"') -and $v.EndsWith('"')) -or ($v.StartsWith("'") -and $v.EndsWith("'")))) {
                $v = $v.Substring(1, $v.Length - 2)
            }
            return $v
        }
    }
    return ""
}

$BackendSet = $PSBoundParameters.ContainsKey("Backend")
$PortSet = $PSBoundParameters.ContainsKey("Port")

if ($BackendSet) {
    $BackendNorm = $Backend.ToLower()
} else {
    $BackendNorm = (Read-ConfigValue "backend_type").ToLower()
    if (-not $BackendNorm) { $BackendNorm = "cli" }
}
if ($BackendNorm -eq "zen") { $BackendNorm = "zed" }
if (@("cli", "zed", "auto") -notcontains $BackendNorm) {
    Fail "无效的 backend_type: $BackendNorm" "Invalid backend_type: $BackendNorm"
}

if (-not $PortSet) {
    if ($env:AG_PORT) {
        $Port = [int]$env:AG_PORT
    } elseif ($Mode -eq "docker") {
        $Port = 8080
    } else {
        $cfgPort = Read-ConfigValue "port"
        $Port = if ($cfgPort) { [int]$cfgPort } else { 8080 }
    }
}
if ($Port -lt 1 -or $Port -gt 65535) {
    Fail "无效的端口: $Port" "Invalid port: $Port"
}

if ($AcpBin -and -not (Test-Path $AcpBin)) {
    Fail "-AcpBin 指定的文件不存在: $AcpBin" "-AcpBin file does not exist: $AcpBin"
}

if ($AdminPasswordStdin) {
    if ($AdminPassword) { Fail "-AdminPassword 与 -AdminPasswordStdin 不能同时使用" "-AdminPassword and -AdminPasswordStdin are mutually exclusive" }
    if ([Console]::IsInputRedirected) {
        $AdminPassword = [Console]::In.ReadLine()
    } else {
        $secure = Read-Host -AsSecureString "Admin password / 管理员密码"
        $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
        try { $AdminPassword = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr) } finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
    }
} elseif ($AdminPassword) {
    Write-BilingualWarning "-AdminPassword 会出现在命令历史与进程列表中，建议改用 -AdminPasswordStdin。" `
                           "-AdminPassword is visible in command history and the process list; prefer -AdminPasswordStdin."
}
if ($AdminPasswordStdin -or $AdminPassword) {
    if (-not $AdminPassword -or $AdminPassword.Length -lt 8) { Fail "管理员密码至少需要 8 个字符" "Admin password must be at least 8 characters" }
    if ($AdminPassword -match "[`r`n]") { Fail "管理员密码不能包含换行符" "Admin password must not contain newlines" }
    Write-Bilingual ">>> 提示: 该密码仅在 Web 控制台「修改密码」之前生效。" `
                    ">>> Note: this password only applies until it is changed in the Web Console."
}

foreach ($k in @("admin_password", "admin_token")) {
    $v = Read-ConfigValue $k
    if ($v -and $v.ToLower() -ne "auto") {
        Write-BilingualWarning "config\config.yaml 中的 $k 不是 `"auto`"，该文件受 git 跟踪，请改回 `"auto`" 并改用 -AdminPasswordStdin / AG_ADMIN_PASSWORD。" `
                               "$k in config\config.yaml is not `"auto`". The file is tracked by git; reset it to `"auto`" and use -AdminPasswordStdin / AG_ADMIN_PASSWORD."
    }
}

function Assert-FileHash([string]$Path, [string]$Algorithm, [string]$Expected, [string]$Label) {
    $Expected = ("" + $Expected).Trim().ToLower()
    if (-not $Expected) {
        if ($InsecureSkipVerify) {
            Write-BilingualWarning "$Label 缺少预期 $Algorithm，因 -InsecureSkipVerify 跳过校验" "No expected $Algorithm for $Label; skipping verification because of -InsecureSkipVerify"
            return
        }
        Fail "$Label 缺少预期 $Algorithm，拒绝安装 (如确需跳过请显式传入 -InsecureSkipVerify)" "No expected $Algorithm for $Label; refusing to install (pass -InsecureSkipVerify to override)"
    }
    $actual = (Get-FileHash -Path $Path -Algorithm $Algorithm).Hash.ToLower()
    if ($actual -ne $Expected) {
        Fail "$Label $Algorithm 校验失败: 预期 $Expected，实际 $actual" "$Label $Algorithm mismatch: expected $Expected, got $actual"
    }
    Write-Bilingual "    -> $Algorithm 校验通过" "    -> $Algorithm verified"
}

function Find-ExistingZedBinary {
    foreach ($c in @("$RootDir\bin\agy_acp_server.exe", "$RootDir\bin\agy_acp_server.par")) {
        if (Test-Path $c) {
            return (Resolve-Path $c).Path
        }
    }
    foreach ($base in @($env:LOCALAPPDATA, $env:APPDATA)) {
        if ($base) {
            $regDir = Join-Path $base "Zed\external_agents\registry\antigravity-acp"
            if (Test-Path $regDir) {
                $found = Get-ChildItem -Path $regDir -Recurse -Filter "agy_acp_server*" -File -ErrorAction SilentlyContinue | Sort-Object FullName
                if ($found) {
                    return $found[-1].FullName
                }
            }
        }
    }
    return ""
}

function Find-ExistingCliBinary {
    foreach ($c in @(
        "$RootDir\bin\agy.exe",
        "$env:LOCALAPPDATA\agy\bin\agy.exe",
        "$env:LOCALAPPDATA\Programs\Antigravity\agy.exe",
        "$env:USERPROFILE\.local\bin\agy.exe",
        "$env:USERPROFILE\.antigravity\bin\agy.exe",
        "C:\Program Files\Google\antigravity-cli\agy.exe"
    )) {
        if ($c -and (Test-Path $c)) {
            return (Resolve-Path $c).Path
        }
    }
    $cliCmd = Get-Command agy -ErrorAction SilentlyContinue
    if (-not $cliCmd) {
        $cliCmd = Get-Command antigravity -ErrorAction SilentlyContinue
    }
    if ($cliCmd) {
        return $cliCmd.Source
    }
    return ""
}

function Install-CliBackend {
    New-Item -ItemType Directory -Force -Path "$RootDir\bin" | Out-Null
    $targetBin = "$RootDir\bin\agy.exe"
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    $platform = if ($arch -eq "ARM64") { "windows_arm64" } else { "windows_amd64" }

    Write-Bilingual ">>> 正在自动下载并安装 Antigravity CLI 后端 ($platform) -> $targetBin ..." `
                    ">>> Auto-downloading & installing Antigravity CLI backend ($platform) -> $targetBin ..."

    $manifestUrl = "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/$platform.json"
    try {
        $manifest = Invoke-RestMethod -Uri $manifestUrl
    } catch {
        Fail "无法获取 CLI 发布清单 ($manifestUrl)，请使用 -AcpBin 指定已安装的 agy.exe" "CLI release manifest unavailable ($manifestUrl); use -AcpBin to point at an installed agy.exe"
    }
    if (-not $manifest.url) { Fail "CLI 发布清单中没有下载地址" "CLI manifest has no package URL" }
    if (-not ("" + $manifest.url).StartsWith("https://")) { Fail "CLI 下载地址不是 HTTPS: $($manifest.url)" "CLI package URL is not HTTPS: $($manifest.url)" }

    $tmpFile = Join-Path $env:TEMP "agy_download_$([guid]::NewGuid().ToString('N')).exe"
    try {
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -UseBasicParsing -Uri $manifest.url -OutFile $tmpFile
        Assert-FileHash -Path $tmpFile -Algorithm SHA512 -Expected $manifest.sha512 -Label "Antigravity CLI $($manifest.version)"
        Move-Item -Force -Path $tmpFile -Destination $targetBin
        Write-Bilingual ">>> Antigravity CLI v$($manifest.version) 安装完成: $targetBin" `
                        ">>> Antigravity CLI v$($manifest.version) installed at: $targetBin"
    } finally {
        if (Test-Path $tmpFile) { Remove-Item -Force $tmpFile -ErrorAction SilentlyContinue }
    }
}

function Install-ZedBackend {
    New-Item -ItemType Directory -Force -Path "$RootDir\bin" | Out-Null
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    $releaseArch = if ($arch -eq "ARM64") { "arm64" } else { "x86_64" }
    $archiveName = "agy-acp-server-$ZedVersion-windows-$releaseArch.zip"
    $archiveUrl = "https://dl.google.com/agy-extensions/releases/windows/$archiveName"

    if (-not $ZedSha256 -and -not $InsecureSkipVerify) {
        Fail ("下载 Zed ACP 包需要固定的 SHA-256。请传入 -ZedSha256 <$archiveName 的 sha256> 或设置环境变量 ZED_ACP_SHA256。" +
              "哈希应来自可信来源，例如 https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json 中 antigravity-acp 条目的 distribution.binary.<目标平台>.sha256；也可用 -AcpBin 指定已安装的 agy_acp_server.exe。") `
             ("Installing the Zed ACP package requires a pinned SHA-256. Pass -ZedSha256 <sha256 of $archiveName> or set ZED_ACP_SHA256. " +
              "Take it from a trusted source, e.g. distribution.binary.<target>.sha256 of the antigravity-acp entry in https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json, or pass -AcpBin with an installed agy_acp_server.exe.")
    }

    Write-Bilingual ">>> 正在下载 Zed ACP 独立后端包 $archiveUrl -> $RootDir\bin ..." `
                    ">>> Downloading Zed ACP standalone package $archiveUrl -> $RootDir\bin ..."

    $tmpZip = Join-Path $env:TEMP "agy_zed_$([guid]::NewGuid().ToString('N')).zip"
    try {
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -UseBasicParsing -Uri $archiveUrl -OutFile $tmpZip
        Assert-FileHash -Path $tmpZip -Algorithm SHA256 -Expected $ZedSha256 -Label $archiveName
        Expand-Archive -Path $tmpZip -DestinationPath "$RootDir\bin" -Force
        Write-Bilingual ">>> Zed ACP 独立后端包安装完成: $RootDir\bin" ">>> Zed ACP standalone package installed in: $RootDir\bin"
    } finally {
        if (Test-Path $tmpZip) { Remove-Item -Force $tmpZip -ErrorAction SilentlyContinue }
    }
}

function Ensure-AcpBackend {
    if ($AcpBin) { return }
    if ($NoDownload) { return }

    switch ($BackendNorm) {
        "zed" {
            if (-not (Find-ExistingZedBinary)) {
                Install-ZedBackend
            }
        }
        "cli" {
            if (-not (Find-ExistingCliBinary)) {
                Install-CliBackend
            }
        }
        default {
            if (-not (Find-ExistingCliBinary) -and -not (Find-ExistingZedBinary)) {
                Install-CliBackend
            }
        }
    }
}

function Resolve-AcpBinary {
    if ($AcpBin) {
        return (Resolve-Path $AcpBin).Path
    }

    $foundCli = Find-ExistingCliBinary
    $foundZed = Find-ExistingZedBinary

    switch ($BackendNorm) {
        "zed" { return $(if ($foundZed) { $foundZed } else { "agy_acp_server.exe" }) }
        "cli" { return $(if ($foundCli) { $foundCli } else { "agy.exe" }) }
        default {
            if ($foundCli) { return $foundCli }
            if ($foundZed) { return $foundZed }
            return "auto"
        }
    }
}

function Build-Gateway {
    Write-Bilingual ">>> [1/3] 构建中英双语前端控制台 (web/dist)..." ">>> [1/3] Building bilingual ReUI frontend (web/dist)..."
    if (-not (Test-Path "$RootDir\web\node_modules")) {
        npm --prefix "$RootDir\web" ci
        if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
    }
    npm --prefix "$RootDir\web" run build
    if ($LASTEXITCODE -ne 0) { throw "npm run build failed" }

    New-Item -ItemType Directory -Force -Path "$RootDir\bin" | Out-Null
    Write-Bilingual ">>> [2/3] 编译 Windows 网关二进制 (bin\antigravity-gateway.exe)..." ">>> [2/3] Building Windows gateway binary (bin\antigravity-gateway.exe)..."
    $env:CGO_ENABLED = "0"
    go build -tags webui -trimpath -ldflags="-s -w" -o $GatewayExe .\cmd\gateway
    if ($LASTEXITCODE -ne 0) { throw "go build (gateway) failed" }
    go build -trimpath -ldflags="-s -w" -o "$RootDir\bin\antigravity-probe.exe" .\cmd\probe
    if ($LASTEXITCODE -ne 0) { throw "go build (probe) failed" }
}

function Set-YamlScalar([string]$Content, [string]$Key, [string]$RenderedValue) {
    $pattern = "(?m)^(\s*$Key\s*:\s*).*$"
    $evaluator = [System.Text.RegularExpressions.MatchEvaluator] ({ param($m) $m.Groups[1].Value + $RenderedValue }.GetNewClosure())
    return [regex]::new($pattern).Replace($Content, $evaluator, 1)
}

function Quote-Yaml([string]$Value) {
    return '"' + $Value.Replace('\', '\\').Replace('"', '\"') + '"'
}

# Persist explicitly requested, non-secret settings. Secrets are never written here.
function Update-GatewayConfig {
    $resolvedBin = (Resolve-AcpBinary).Replace("\", "/")
    $rootFwd = $RootDir.Replace("\", "/") + "/"
    if ($resolvedBin.StartsWith($rootFwd, [StringComparison]::OrdinalIgnoreCase)) {
        $resolvedBin = "./" + $resolvedBin.Substring($rootFwd.Length)
    }
    Write-Bilingual ">>> [3/3] 同步配置 config\config.yaml (backend_type=$BackendNorm, binary_path=$resolvedBin, port=$Port)..." `
                    ">>> [3/3] Updating config\config.yaml (backend_type=$BackendNorm, binary_path=$resolvedBin, port=$Port)..."

    $content = [System.IO.File]::ReadAllText($CfgPath)
    $original = $content
    if ($BackendSet) {
        if ($content -match "(?m)^\s*backend_type\s*:") {
            $content = Set-YamlScalar $content "backend_type" (Quote-Yaml $BackendNorm)
        } else {
            $content = [regex]::Replace($content, '(?m)^(acp_pool\s*:\s*\r?\n)', "`$1  backend_type: `"$BackendNorm`"`n")
        }
    }
    if ($resolvedBin -and $resolvedBin -ne (Read-ConfigValue "binary_path")) {
        $content = Set-YamlScalar $content "binary_path" (Quote-Yaml $resolvedBin)
    }
    if ($PortSet) {
        $content = [regex]::Replace($content, '(?m)^(\s*port\s*:\s*)\d+', "`${1}$Port")
        $content = [regex]::Replace($content, '(?m)^(\s*public_base_url\s*:\s*)"http://localhost:\d+"', "`${1}`"http://localhost:$Port`"")
    }
    if ($content -ne $original) {
        [System.IO.File]::WriteAllText($CfgPath, $content, (New-Object System.Text.UTF8Encoding $false))
    }
}

function Get-DataDir {
    $d = Read-ConfigValue "data_dir"
    if (-not $d) { $d = "./data" }
    if ([System.IO.Path]::IsPathRooted($d)) { return $d }
    return [System.IO.Path]::GetFullPath((Join-Path $RootDir $d))
}

# Equivalent of chmod 700: only the current user (no inherited ACEs).
function Protect-Path([string]$Path) {
    try {
        & icacls "$Path" /inheritance:r /grant:r "$($env:USERDOMAIN)\$($env:USERNAME):(OI)(CI)F" | Out-Null
    } catch {
        Write-BilingualWarning "无法收紧 $Path 的访问权限: $_" "Could not restrict permissions on ${Path}: $_"
    }
}

function Initialize-DataDir {
    $dataDir = Get-DataDir
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    Protect-Path $dataDir
    return $dataDir
}

# Returns the process only if it is our gateway executable.
function Get-OwnGatewayProcess([int]$ProcessId) {
    $p = Get-Process -Id $ProcessId -ErrorAction SilentlyContinue
    if (-not $p) { return $null }
    $path = ""
    try { $path = $p.Path } catch { $path = "" }
    if ($path -and ([System.IO.Path]::GetFullPath($path) -ieq [System.IO.Path]::GetFullPath($GatewayExe))) {
        return $p
    }
    return $null
}

function Get-PortOwners {
    try {
        return @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction Stop | Select-Object -ExpandProperty OwningProcess -Unique)
    } catch {
        return @()
    }
}

function Report-PortOwners([int]$OwnPid = 0) {
    foreach ($ownerPid in Get-PortOwners) {
        if ($ownerPid -eq $OwnPid) { continue }
        $name = (Get-Process -Id $ownerPid -ErrorAction SilentlyContinue).Path
        Write-BilingualWarning "端口 $Port 被 PID $ownerPid 占用 (非本网关进程): $name" "Port $Port is busy by PID $ownerPid (not ours): $name"
    }
}

function Wait-Healthz([int]$TimeoutSec, [int]$ProcessId = 0) {
    $host_ = Read-ConfigValue "host"
    if (-not $host_ -or $host_ -eq "0.0.0.0" -or $host_ -eq "::") { $host_ = "127.0.0.1" }
    $url = "http://${host_}:$Port/healthz"
    for ($i = 0; $i -lt $TimeoutSec; $i++) {
        if ($ProcessId -gt 0 -and -not (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue)) { return 2 }
        try {
            $resp = Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -Uri $url
            if ($resp.StatusCode -eq 200) { return 0 }
        } catch { }
        Start-Sleep -Seconds 1
    }
    return 1
}

function Show-CredentialsHint {
    $credFile = Join-Path (Get-DataDir) "admin_credentials.txt"
    if ($Mode -eq "docker") {
        $credCmd = "docker exec antigravity-gateway cat /var/lib/antigravity-gateway/admin_credentials.txt"
    } else {
        $credCmd = "Get-Content `"$credFile`""
        if (Test-Path $credFile) { Get-Content $credFile }
    }
    Write-Bilingual ">>> 随时查看凭证命令: $credCmd (凭证不会写入日志)" ">>> View credentials anytime via: $credCmd (credentials are never logged)"
    Write-Bilingual ">>> 北向 API (/v1/*) 仅允许本机回环地址免密访问；远程客户端请先在控制台创建 API Key。" `
                    ">>> Northbound API (/v1/*) is only open without a key from loopback; create an API key in the console for remote clients."
}

function Stop-LocalGateway {
    $pidFile = Join-Path (Get-DataDir) "gateway.pid"
    if (Test-Path $pidFile) {
        $oldPid = 0
        [void][int]::TryParse(("" + (Get-Content $pidFile -ErrorAction SilentlyContinue)).Trim(), [ref]$oldPid)
        $proc = if ($oldPid -gt 0) { Get-OwnGatewayProcess $oldPid } else { $null }
        if ($proc) {
            Stop-Process -Id $oldPid -Force -ErrorAction SilentlyContinue
            [void]$proc.WaitForExit(15000)
            Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
            Write-Bilingual ">>> 已停止网关进程 (PID: $oldPid)" ">>> Stopped gateway process (PID: $oldPid)"
            return
        }
        if ($oldPid -gt 0 -and (Get-Process -Id $oldPid -ErrorAction SilentlyContinue)) {
            Write-BilingualWarning "PID 文件中的进程 $oldPid 不是 $GatewayExe，不会终止它" "PID $oldPid from the pid file is not $GatewayExe; not stopping it"
        }
        Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
    }
    Write-Bilingual ">>> 未发现运行中的网关进程" ">>> No running gateway process found"
    Report-PortOwners
}

function Start-LocalGateway {
    if (-not (Test-Path $GatewayExe)) { Fail "未找到 $GatewayExe，请先执行 -Action build" "$GatewayExe not found; run -Action build first" }
    $dataDir = Initialize-DataDir
    $pidFile = Join-Path $dataDir "gateway.pid"
    Stop-LocalGateway

    if ((Get-PortOwners).Count -gt 0) {
        Report-PortOwners
        Fail "端口 $Port 已被占用，无法启动" "Port $Port is already in use; cannot start"
    }

    $argList = @("-config", "`"$CfgPath`"")
    if ($BackendSet) { $argList += @("-backend", $BackendNorm) }
    if ($AcpBin) { $argList += @("-acp-bin", "`"$AcpBin`"") }

    $prevPwd = $env:AG_ADMIN_PASSWORD
    try {
        if ($AdminPassword) { $env:AG_ADMIN_PASSWORD = $AdminPassword }
        $proc = Start-Process -FilePath $GatewayExe `
            -ArgumentList $argList `
            -WorkingDirectory $RootDir `
            -RedirectStandardOutput (Join-Path $dataDir "gateway.out.log") `
            -RedirectStandardError (Join-Path $dataDir "gateway.err.log") `
            -WindowStyle Hidden `
            -PassThru
    } finally {
        $env:AG_ADMIN_PASSWORD = $prevPwd
    }

    Set-Content -Path $pidFile -Value $proc.Id
    $rc = Wait-Healthz 20 $proc.Id
    if ($rc -ne 0) {
        if ($rc -eq 2) { Remove-Item $pidFile -Force -ErrorAction SilentlyContinue }
        Fail "启动失败或 20 秒内未通过 /healthz，请检查 $dataDir\gateway.err.log" "Startup failed or /healthz did not pass within 20s; check $dataDir\gateway.err.log"
    }
    Write-Bilingual ">>> Antigravity ACP Gateway 已在后台启动! PID: $($proc.Id) | 访问: http://localhost:$Port" `
                    ">>> Antigravity ACP Gateway started in background! PID: $($proc.Id) | URL: http://localhost:$Port"
    Show-CredentialsHint
}

function Status-LocalGateway {
    $pidFile = Join-Path (Get-DataDir) "gateway.pid"
    if (Test-Path $pidFile) {
        $p = 0
        [void][int]::TryParse(("" + (Get-Content $pidFile)).Trim(), [ref]$p)
        if ($p -gt 0 -and (Get-OwnGatewayProcess $p)) {
            Write-Bilingual ">>> 运行中 (PID: $p) | http://localhost:$Port" ">>> Running (PID: $p) | http://localhost:$Port"
            Report-PortOwners $p
            return
        }
    }
    Write-Bilingual ">>> 未运行" ">>> Stopped"
    Report-PortOwners
}

# Wrapper so the scheduled task gets a working directory, a log file and (optionally) the admin password.
function Write-ServiceWrapper {
    $dataDir = Initialize-DataDir
    $wrapper = Join-Path $dataDir "run-gateway.cmd"
    $lines = @(
        "@echo off",
        "cd /d `"$RootDir`""
    )
    if ($AdminPassword) {
        if ($AdminPassword.Contains('"')) { Fail "service 模式下管理员密码不能包含双引号" "In service mode the admin password must not contain a double quote" }
        $lines += "set `"AG_ADMIN_PASSWORD=$($AdminPassword.Replace('%', '%%'))`""
    }
    $lines += "`"$GatewayExe`" -config `"$CfgPath`" >> `"$dataDir\gateway.log`" 2>&1"
    [System.IO.File]::WriteAllLines($wrapper, $lines, (New-Object System.Text.UTF8Encoding $false))
    Protect-Path $wrapper
    return $wrapper
}

function Install-ServiceTask {
    $wrapper = Write-ServiceWrapper
    $action = New-ScheduledTaskAction -Execute "$env:SystemRoot\System32\cmd.exe" -Argument "/d /c `"$wrapper`"" -WorkingDirectory $RootDir
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId "$($env:USERDOMAIN)\$($env:USERNAME)" -LogonType S4U -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    Restart-ServiceTask
    Write-Bilingual ">>> 已注册并启动 Windows 计划任务 (开机启动): $TaskName" ">>> Registered and started Windows Scheduled Task (at startup): $TaskName"
}

function Stop-ServiceTask {
    Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    foreach ($p in Get-Process -Name "antigravity-gateway" -ErrorAction SilentlyContinue) {
        if (Get-OwnGatewayProcess $p.Id) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
            [void]$p.WaitForExit(15000)
        }
    }
}

function Restart-ServiceTask {
    if ($AdminPassword) { [void](Write-ServiceWrapper) }
    Stop-ServiceTask
    Start-ScheduledTask -TaskName $TaskName
    if ((Wait-Healthz 20) -ne 0) {
        Fail "计划任务 20 秒内未通过 /healthz，请检查 $(Get-DataDir)\gateway.log" "Scheduled task did not pass /healthz within 20s; check $(Get-DataDir)\gateway.log"
    }
    Show-CredentialsHint
}

function Set-DockerEnv {
    $env:AG_ACP_BACKEND = $BackendNorm
    $env:AG_PORT = "$Port"
    $env:ZED_ACP_VERSION = $ZedVersion
    if ($ZedSha256) { $env:ZED_ACP_SHA256 = $ZedSha256 }
    if ($InsecureSkipVerify) { $env:AG_INSECURE_SKIP_VERIFY = "1" }
    if (-not $env:AG_BIND) {
        Write-Bilingual ">>> 端口仅绑定 127.0.0.1:$Port；如需对外开放请显式设置 `$env:AG_BIND=`"0.0.0.0`"" `
                        ">>> Port is published on 127.0.0.1:$Port only; set `$env:AG_BIND=`"0.0.0.0`" explicitly to expose it"
    }
    if ($AdminPassword) {
        if ($AdminPassword.Contains("'")) { Fail "Docker 模式下管理员密码不能包含单引号 (')" "In docker mode the admin password must not contain a single quote (')" }
        $lines = @(
            "# Managed by deploy/deploy.ps1 (gitignored). Applies only until the password is changed in the Web Console.",
            "AG_ADMIN_PASSWORD='$AdminPassword'"
        )
        [System.IO.File]::WriteAllLines($DockerEnvFile, $lines, (New-Object System.Text.UTF8Encoding $false))
        Protect-Path $DockerEnvFile
    }
    New-Item -ItemType Directory -Force -Path "$RootDir\bin" | Out-Null
}

function Invoke-Compose([string[]]$ComposeArgs) {
    docker compose -f $ComposeFile @ComposeArgs
    if ($LASTEXITCODE -ne 0) { throw "docker compose $($ComposeArgs -join ' ') failed" }
}

function Invoke-Lifecycle([string]$Verb) {
    switch ($Mode) {
        "service" {
            switch ($Verb) {
                "start" { Restart-ServiceTask }
                "restart" { Restart-ServiceTask }
                "stop" { Stop-ServiceTask; Write-Bilingual ">>> 已停止计划任务 $TaskName" ">>> Stopped scheduled task $TaskName" }
                "status" { Get-ScheduledTask -TaskName $TaskName | Select-Object TaskName, State | Format-Table -AutoSize; Status-LocalGateway }
            }
        }
        "docker" {
            switch ($Verb) {
                "start" { Set-DockerEnv; Invoke-Compose @("up", "-d"); Show-CredentialsHint }
                "restart" { Set-DockerEnv; Invoke-Compose @("up", "-d", "--force-recreate"); Show-CredentialsHint }
                "stop" { Invoke-Compose @("down") }
                "status" { Invoke-Compose @("ps") }
            }
        }
        default {
            switch ($Verb) {
                "start" { Ensure-AcpBackend; Update-GatewayConfig; Start-LocalGateway }
                "restart" { Ensure-AcpBackend; Update-GatewayConfig; Start-LocalGateway }
                "stop" { Stop-LocalGateway }
                "status" { Status-LocalGateway }
            }
        }
    }
}

switch ($Action) {
    "install-acp" {
        if ($BackendNorm -eq "zed") {
            Install-ZedBackend
        } else {
            Install-CliBackend
        }
    }
    "build" {
        Build-Gateway
        Ensure-AcpBackend
        Update-GatewayConfig
    }
    "install" {
        if ($Mode -eq "docker") {
            if ($AcpBin) {
                New-Item -ItemType Directory -Force -Path "$RootDir\bin" | Out-Null
                Copy-Item -Force $AcpBin "$RootDir\bin\"
                $env:AG_ACP_BINARY_PATH = "/opt/antigravity-acp/$(Split-Path -Leaf $AcpBin)"
            }
            if ($BackendNorm -eq "zed" -and -not $ZedSha256 -and -not $InsecureSkipVerify) {
                Fail "Docker 构建 Zed 后端需要 -ZedSha256 (或 ZED_ACP_SHA256)" "Building the Zed backend image requires -ZedSha256 (or ZED_ACP_SHA256)"
            }
            Set-DockerEnv
            Invoke-Compose @("up", "-d", "--build")
            Show-CredentialsHint
        } elseif ($Mode -eq "service") {
            Build-Gateway
            Ensure-AcpBackend
            Update-GatewayConfig
            Install-ServiceTask
        } else {
            Build-Gateway
            Ensure-AcpBackend
            Update-GatewayConfig
            Start-LocalGateway
        }
    }
    default { Invoke-Lifecycle $Action }
}
