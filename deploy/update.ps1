# ==============================================================================
# Antigravity ACP Gateway — Windows PowerShell One-Click Update Script
#
# Pulls the latest repository code (git pull), updates the ACP backend package
# (Antigravity CLI `agy.exe` and/or Zed standalone `agy_acp_server.exe`),
# rebuilds the frontend + Go gateway, and restarts the active deployment
# (local / Windows Scheduled Task service / Docker).
#
# Usage:
#   .\deploy\update.ps1                        # Auto-detect mode & backend, update all
#   .\deploy\update.ps1 -Backend cli           # Update code + Antigravity CLI (agy.exe)
#   .\deploy\update.ps1 -Backend zed           # Update code + Zed ACP package
#   .\deploy\update.ps1 -Backend both          # Update code + both CLI and Zed ACP packages
#   .\deploy\update.ps1 -Action update-acp     # Update ACP backend package(s) only
#   .\deploy\update.ps1 -NoGit                 # Update ACP & rebuild without git pull
# ==============================================================================
param(
    [ValidateSet("update", "update-acp", "install-acp")]
    [string]$Action = "update",

    [ValidateSet("local", "service", "docker")]
    [string]$Mode = "local",

    [ValidateSet("cli", "zed", "zen", "auto", "both", "all")]
    [string]$Backend = "cli",

    [string]$AcpBin = "",
    [switch]$NoDownload,
    [switch]$NoGit,
    [int]$Port = 0,
    [string]$AdminPassword = "",
    [switch]$AdminPasswordStdin,
    [string]$ZedSha256 = $env:ZED_ACP_SHA256,
    [string]$ZedVersion = $(if ($env:ZED_ACP_VERSION) { $env:ZED_ACP_VERSION } else { "1.4.5" }),
    [switch]$InsecureSkipVerify,
    [ValidateSet("zh", "en")]
    [string]$Lang = "zh"
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$forwardParams = @{}
foreach ($kv in $PSBoundParameters.GetEnumerator()) {
    $forwardParams[$kv.Key] = $kv.Value
}
if (-not $forwardParams.ContainsKey("Action")) {
    $forwardParams["Action"] = "update"
}

& (Join-Path $ScriptDir "deploy.ps1") @forwardParams
