# ==============================================================================
# Antigravity ACP Gateway — Windows PowerShell Deployment Script
# Supports: Windows 10/11 & Windows Server (x64 / ARM64) + Docker Desktop
# Supports Automatic ACP Backend Download & Installation:
#   - cli (default) : Antigravity CLI (agy.exe acp)
#   - zed / zen     : Zed/Zen standalone ACP backend package (agy_acp_server.exe)
#   - auto          : Auto-discover CLI (agy.exe) or Zed/Zen ACP package, auto-installing CLI if missing
# ==============================================================================
param(
    [ValidateSet("install", "install-acp", "start", "stop", "restart", "status", "build")]
    [string]$Action = "install",

    [ValidateSet("local", "service", "docker")]
    [string]$Mode = "local",

    [ValidateSet("cli", "zed", "zen", "auto")]
    [string]$Backend = "cli",

    [string]$AcpBin = "",
    [switch]$NoDownload,
    [int]$Port = 8080,
    [string]$AdminPassword = "",

    [ValidateSet("zh", "en")]
    [string]$Lang = "zh"
)

$ErrorActionPreference = "Stop"
$RootDir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $RootDir

$BackendNorm = $Backend.ToLower()
if ($BackendNorm -eq "zen") {
    $BackendNorm = "zed"
}

function Write-Bilingual([string]$Zh, [string]$En) {
    if ($Lang -eq "en") {
        Write-Host $En -ForegroundColor Cyan
    } else {
        Write-Host $Zh -ForegroundColor Cyan
    }
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
                $matches = Get-ChildItem -Path $regDir -Recurse -Filter "agy_acp_server*" -File -ErrorAction SilentlyContinue | Sort-Object FullName
                if ($matches) {
                    return $matches[-1].FullName
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
    $manifest = Invoke-RestMethod -Uri $manifestUrl
    $tmpFile = Join-Path $env:TEMP "agy_download_$([guid]::NewGuid().ToString('N')).exe"
    try {
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -Uri $manifest.url -OutFile $tmpFile
        if ($manifest.sha512) {
            $actualHash = (Get-FileHash -Path $tmpFile -Algorithm SHA512).Hash.ToLower()
            if ($actualHash -ne $manifest.sha512.ToLower()) {
                throw "SHA512 checksum mismatch for $($manifest.url)"
            }
        }
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
    $targetKey = if ($arch -eq "ARM64") { "windows-aarch64" } else { "windows-x86_64" }
    $fallbackArch = if ($arch -eq "ARM64") { "arm64" } else { "x86_64" }
    $archiveUrl = "https://dl.google.com/agy-extensions/releases/windows/agy-acp-server-1.4.5-windows-$fallbackArch.zip"
    $expectedSha256 = ""

    Write-Bilingual ">>> 正在自动下载并安装 Zed ACP 独立后端包 ($targetKey) -> $RootDir\bin ..." `
                    ">>> Auto-downloading & installing Zed ACP standalone package ($targetKey) -> $RootDir\bin ..."

    try {
        $reg = Invoke-RestMethod -Uri "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"
        foreach ($ag in $reg.agents) {
            if ($ag.id -eq "antigravity-acp") {
                $dist = $ag.distribution.binary.$targetKey
                if ($dist -and $dist.archive) {
                    $archiveUrl = $dist.archive
                    $expectedSha256 = $dist.sha256
                }
                break
            }
        }
    } catch {
        # Fallback to direct URL
    }

    $tmpZip = Join-Path $env:TEMP "agy_zed_$([guid]::NewGuid().ToString('N')).zip"
    try {
        $ProgressPreference = 'SilentlyContinue'
        Invoke-WebRequest -Uri $archiveUrl -OutFile $tmpZip
        if ($expectedSha256) {
            $actualHash = (Get-FileHash -Path $tmpZip -Algorithm SHA256).Hash.ToLower()
            if ($actualHash -ne $expectedSha256.ToLower()) {
                throw "SHA256 checksum mismatch for $archiveUrl"
            }
        }
        Expand-Archive -Path $tmpZip -DestinationPath "$RootDir\bin" -Force
        Write-Bilingual ">>> Zed ACP 独立后端包安装完成: $RootDir\bin\agy_acp_server.exe" `
                        ">>> Zed ACP standalone package installed at: $RootDir\bin\agy_acp_server.exe"
    } finally {
        if (Test-Path $tmpZip) { Remove-Item -Force $tmpZip -ErrorAction SilentlyContinue }
    }
}

function Ensure-AcpBackend {
    if ($AcpBin -and (Test-Path $AcpBin)) { return }
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
    if ($AcpBin -and (Test-Path $AcpBin)) {
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
        npm --prefix "$RootDir\web" install
    }
    npm --prefix "$RootDir\web" run build

    New-Item -ItemType Directory -Force -Path "$RootDir\bin", "$RootDir\data" | Out-Null
    Write-Bilingual ">>> [2/3] 编译 Windows 网关二进制 (bin\antigravity-gateway.exe)..." ">>> [2/3] Building Windows gateway binary (bin\antigravity-gateway.exe)..."
    $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags="-s -w" -o "$RootDir\bin\antigravity-gateway.exe" .\cmd\gateway
    go build -trimpath -ldflags="-s -w" -o "$RootDir\bin\antigravity-probe.exe" .\cmd\probe
}

function Update-GatewayConfig {
    $resolvedBin = if ($Mode -eq "docker" -and -not $AcpBin) { "auto" } else { Resolve-AcpBinary }
    Write-Bilingual ">>> [3/3] 更新配置文件 config\config.yaml (backend_type=$BackendNorm, binary_path=$resolvedBin, port=$Port)..." `
                    ">>> [3/3] Updating config\config.yaml (backend_type=$BackendNorm, binary_path=$resolvedBin, port=$Port)..."

    $cfgPath = "$RootDir\config\config.yaml"
    $content = Get-Content -Path $cfgPath -Raw -Encoding UTF8
    if ($content -match "(?m)^\s*backend_type\s*:") {
        $content = [regex]::Replace($content, '(?m)^(\s*backend_type\s*:\s*).*$', "`$1`"$BackendNorm`"")
    } else {
        $content = [regex]::Replace($content, '(?m)^(acp_pool\s*:\s*\r?\n)', "`$1  backend_type: `"$BackendNorm`"`n")
    }
    if ($resolvedBin) {
        $escaped = $resolvedBin.Replace("\", "/")
        $content = [regex]::Replace($content, '(?m)^(\s*binary_path\s*:\s*).*$', "`$1`"$escaped`"")
    }
    if ($Port -gt 0) {
        $content = [regex]::Replace($content, '(?m)^(\s*port\s*:\s*)\d+', "`${1}$Port")
        $content = [regex]::Replace($content, '(?m)^(\s*public_base_url\s*:\s*)"http://localhost:\d+"', "`$1`"http://localhost:$Port`"")
    }
    if ($AdminPassword) {
        $content = [regex]::Replace($content, '(?m)^(\s*admin_password\s*:\s*).*$', "`$1`"$AdminPassword`"")
    }
    Set-Content -Path $cfgPath -Value $content -Encoding UTF8
}

function Start-LocalGateway {
    New-Item -ItemType Directory -Force -Path "$RootDir\data" | Out-Null
    $pidFile = "$RootDir\data\gateway.pid"
    if (Test-Path $pidFile) {
        $oldPid = Get-Content $pidFile -ErrorAction SilentlyContinue
        if ($oldPid) {
            Stop-Process -Id ([int]$oldPid) -Force -ErrorAction SilentlyContinue
        }
    }

    $argList = @("-config", "$RootDir\config\config.yaml", "-backend", $BackendNorm)
    if ($AcpBin) {
        $argList += @("-acp-bin", $AcpBin)
    }

    $proc = Start-Process -FilePath "$RootDir\bin\antigravity-gateway.exe" `
        -ArgumentList $argList `
        -WorkingDirectory $RootDir `
        -RedirectStandardOutput "$RootDir\data\gateway.out.log" `
        -RedirectStandardError "$RootDir\data\gateway.err.log" `
        -WindowStyle Hidden `
        -PassThru

    Set-Content -Path $pidFile -Value $proc.Id
    Write-Bilingual ">>> Antigravity ACP Gateway 已在后台启动! PID: $($proc.Id) | 访问: http://localhost:$Port" `
                    ">>> Antigravity ACP Gateway started in background! PID: $($proc.Id) | URL: http://localhost:$Port"
}

function Stop-LocalGateway {
    $pidFile = "$RootDir\data\gateway.pid"
    if (Test-Path $pidFile) {
        $oldPid = Get-Content $pidFile -ErrorAction SilentlyContinue
        if ($oldPid) {
            Stop-Process -Id ([int]$oldPid) -Force -ErrorAction SilentlyContinue
            Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
            Write-Bilingual ">>> 已停止网关进程 (PID: $oldPid)" ">>> Stopped gateway process (PID: $oldPid)"
            return
        }
    }
    Write-Bilingual ">>> 未发现运行中的网关进程" ">>> No running gateway process found"
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
            Update-GatewayConfig
            $env:AG_ACP_BACKEND = $BackendNorm
            $env:AG_PORT = "$Port"
            if ($AdminPassword) { $env:AG_ADMIN_PASSWORD = $AdminPassword }
            docker compose -f "$RootDir\deploy\docker-compose.yml" up -d --build
        } elseif ($Mode -eq "service") {
            Build-Gateway
            Ensure-AcpBackend
            Update-GatewayConfig
            $taskName = "AntigravityACPGateway"
            $tr = "`"$RootDir\bin\antigravity-gateway.exe`" -config `"$RootDir\config\config.yaml`" -backend $BackendNorm"
            schtasks /Create /F /TN $taskName /SC ONLOGON /TR $tr | Out-Null
            schtasks /Run /TN $taskName | Out-Null
            Write-Bilingual ">>> 已注册并启动 Windows 计划任务服务: $taskName" ">>> Registered and started Windows Scheduled Task: $taskName"
        } else {
            Build-Gateway
            Ensure-AcpBackend
            Update-GatewayConfig
            Start-LocalGateway
        }
    }
    "start" { Ensure-AcpBackend; Start-LocalGateway }
    "stop" { Stop-LocalGateway }
    "restart" { Stop-LocalGateway; Ensure-AcpBackend; Start-LocalGateway }
    "status" {
        $pidFile = "$RootDir\data\gateway.pid"
        if (Test-Path $pidFile) {
            $p = Get-Process -Id ([int](Get-Content $pidFile)) -ErrorAction SilentlyContinue
            if ($p) {
                Write-Bilingual ">>> 运行中 (PID: $($p.Id)) | http://localhost:$Port" ">>> Running (PID: $($p.Id)) | http://localhost:$Port"
                exit 0
            }
        }
        Write-Bilingual ">>> 未运行" ">>> Stopped"
    }
}
