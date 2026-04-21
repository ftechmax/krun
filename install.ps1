#Requires -Version 5.1
<#
.SYNOPSIS
    Installs krun + krun-helper on Windows.
.DESCRIPTION
    Drops binaries into %LOCALAPPDATA%\Programs\krun, adds dir to user PATH,
    registers krun-helper as a Windows Service running as LocalSystem.
.PARAMETER Version
    Release tag to install (e.g. v1.2.3). Defaults to "latest".
    Use "debug" to build local binaries and apply the local runtime overlay.
.PARAMETER Kubeconfig
    Kubeconfig to use for traffic-manager deploy. Defaults to $HOME\.kube\config.
.PARAMETER SkipTrafficManager
    Skips in-cluster traffic-manager deploy/upgrade.
.PARAMETER Uninstall
    Removes the service, binaries, and PATH entry.
#>
[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [Alias('kubeconfig')]
    [string]$Kubeconfig,
    [Alias('skip-traffic-manager')]
    [switch]$SkipTrafficManager,
    [switch]$Uninstall,
    [Parameter(DontShow)]
    [switch]$ConfigBootstrapped
)

$ErrorActionPreference = 'Stop'

$Repo        = 'ftechmax/krun'
$AssetName   = 'krun_windows_amd64.zip'
$TrafficManagerManifest = 'krun-traffic-manager.yaml'
$ServiceName = 'krun-helper'
$InstallDir  = Join-Path $env:LOCALAPPDATA 'Programs\krun'
$ConfigDir   = Join-Path $HOME '.krun'
$ConfigPath  = Join-Path $ConfigDir 'config.json'
$TokenPath   = Join-Path $ConfigDir 'token.bin'
$RepoRoot    = $PSScriptRoot

if (-not $Kubeconfig) {
    $Kubeconfig = Join-Path $HOME '.kube\config'
}

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    ([Security.Principal.WindowsPrincipal]::new($id)).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Invoke-Elevated {
    $argList = @('-NoProfile','-ExecutionPolicy','Bypass','-File',"`"$PSCommandPath`"")
    foreach ($k in $PSBoundParameters.Keys) {
        $v = $PSBoundParameters[$k]
        if ($v -is [switch]) {
            if ($v.IsPresent) { $argList += "-$k" }
        } else {
            $argList += @("-$k", "`"$v`"")
        }
    }
    if (-not $Uninstall -and -not $ConfigBootstrapped) {
        $argList += '-ConfigBootstrapped'
    }
    Write-Host 'Elevation required, relaunching as admin...'
    Start-Process powershell -Verb RunAs -ArgumentList $argList -Wait
    exit
}

function Install-DefaultConfig {
    New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
    if (Test-Path $ConfigPath) {
        Write-Host "Existing config left unchanged: $ConfigPath"
        return
    }

    $json = @'
{
  "source": {
    "path": "~/git/",
    "search_depth": 2
  },
  "local_registry": "registry:5000",
  "remote_registry": "docker.io/ftechmax"
}
'@
    $utf8NoBom = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText($ConfigPath, $json, $utf8NoBom)
    Write-Host "Created default config: $ConfigPath"
}

function Write-RandomSecret([string]$Path) {
    $bytes = [byte[]]::new(32)
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($bytes)
    } finally {
        $rng.Dispose()
    }
    [System.IO.File]::WriteAllBytes($Path, $bytes)
}

function Install-AuthToken {
    New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
    if (Test-Path $TokenPath) {
        Write-Host "Existing auth token left unchanged: $TokenPath"
        return
    }

    Write-RandomSecret $TokenPath
    Write-Host "Created auth token: $TokenPath"
}

function Stop-HelperService {
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status -ne 'Stopped') {
        Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
        # Wait for full stop so binaries can be overwritten.
        for ($i = 0; $i -lt 30; $i++) {
            $svc.Refresh()
            if ($svc.Status -eq 'Stopped') { break }
            Start-Sleep -Milliseconds 500
        }
    }
}

function Remove-HelperService {
    Stop-HelperService
    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($svc) {
        & sc.exe delete $ServiceName | Out-Null
        # SCM marks for delete; handle release may take a moment.
        Start-Sleep -Seconds 1
    }
}

function Add-UserPath([string]$dir) {
    $current = [Environment]::GetEnvironmentVariable('PATH', 'User')
    $parts = if ($current) { $current.Split(';') | Where-Object { $_ } } else { @() }
    if ($parts -notcontains $dir) {
        $new = (@($parts) + $dir) -join ';'
        [Environment]::SetEnvironmentVariable('PATH', $new, 'User')
        Write-Host "Added $dir to user PATH."
    }
}

function Remove-UserPath([string]$dir) {
    $current = [Environment]::GetEnvironmentVariable('PATH', 'User')
    if (-not $current) { return }
    $new = ($current.Split(';') | Where-Object { $_ -and $_ -ne $dir }) -join ';'
    [Environment]::SetEnvironmentVariable('PATH', $new, 'User')
}

function Get-DownloadUrl {
    if ($Version -eq 'latest') {
        return "https://github.com/$Repo/releases/latest/download/$AssetName"
    }
    return "https://github.com/$Repo/releases/download/$Version/$AssetName"
}

function Get-TrafficManagerManifestUrl {
    if ($Version -eq 'latest') {
        return "https://github.com/$Repo/releases/latest/download/$TrafficManagerManifest"
    }
    return "https://github.com/$Repo/releases/download/$Version/$TrafficManagerManifest"
}

function Invoke-DebugBuild {
    $makefile = Join-Path $RepoRoot 'Makefile'
    if (-not (Test-Path $makefile)) {
        throw "Version 'debug' requires a source checkout with Makefile at $RepoRoot"
    }
    if (-not (Get-Command make -ErrorAction SilentlyContinue)) {
        throw 'Required command not found: make'
    }

    Push-Location $RepoRoot
    try {
        Write-Host 'Building debug binaries with make build-windows...'
        & make build-windows
        if ($LASTEXITCODE -ne 0) { throw "make build-windows failed (exit $LASTEXITCODE)" }

        $patchScript = Join-Path $RepoRoot 'scripts\patch-helper-windows.ps1'
        if (-not (Test-Path $patchScript)) { throw "Patch script not found: $patchScript" }

        Write-Host 'Patching krun-helper.exe UAC manifest...'
        & powershell -NoProfile -ExecutionPolicy Bypass -File $patchScript
        if ($LASTEXITCODE -ne 0) { throw "patch-helper-windows.ps1 failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }
}

function Install-Binaries {
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

    if ($Version -eq 'debug') {
        Invoke-DebugBuild
        foreach ($name in @('krun.exe','krun-helper.exe')) {
            $p = Join-Path $RepoRoot $name
            if (-not (Test-Path $p)) { throw "Missing $p" }
            Copy-Item $p $InstallDir -Force
        }
        return
    }

    $url = Get-DownloadUrl
    $zip = Join-Path $env:TEMP "krun-$([guid]::NewGuid()).zip"
    try {
        Write-Host "Downloading $url"
        Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
        Expand-Archive -Path $zip -DestinationPath $InstallDir -Force
    } finally {
        if (Test-Path $zip) { Remove-Item $zip -Force }
    }
}

function Invoke-Kubectl([string[]]$Arguments) {
    & kubectl @Arguments
    if ($LASTEXITCODE -ne 0) { throw "kubectl failed (exit $LASTEXITCODE)" }
}

function Deploy-TrafficManager {
    if ($SkipTrafficManager) {
        Write-Host 'Skipping traffic-manager deploy.'
        return
    }

    if (-not (Get-Command kubectl -ErrorAction SilentlyContinue)) {
        throw 'Required command not found: kubectl'
    }
    if ($Kubeconfig -and -not (Test-Path $Kubeconfig)) {
        throw "Kubeconfig not found: $Kubeconfig. Use -Kubeconfig <path> or -SkipTrafficManager."
    }

    $kubectlArgs = @()
    if ($Kubeconfig) {
        $kubectlArgs += @('--kubeconfig', $Kubeconfig)
    }

    if ($Version -eq 'debug') {
        $overlay = Join-Path $RepoRoot 'deploy\runtime\overlays\local'
        if (-not (Test-Path $overlay)) { throw "Local traffic-manager overlay not found: $overlay" }

        Write-Host "Applying local traffic-manager overlay: $overlay"
        Invoke-Kubectl (@($kubectlArgs) + @('apply', '-k', $overlay))
        return
    }

    $url = Get-TrafficManagerManifestUrl
    Write-Host "Applying traffic-manager manifest: $url"
    Invoke-Kubectl (@($kubectlArgs) + @('apply', '-f', $url))
}

function Register-HelperService {
    $exe = Join-Path $InstallDir 'krun-helper.exe'
    if (-not (Test-Path $exe)) { throw "krun-helper.exe missing at $exe" }

    $binPath = "`"$exe`" --service --config-path `"$ConfigDir`""

    & sc.exe create $ServiceName binPath= $binPath start= auto DisplayName= 'krun helper' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "sc.exe create failed (exit $LASTEXITCODE)" }

    & sc.exe description $ServiceName 'Elevated daemon for krun (runs as LocalSystem)' | Out-Null
    & sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/5000/restart/5000 | Out-Null

    Start-Service -Name $ServiceName
    Write-Host "Service '$ServiceName' registered and started."
}

if (-not $Uninstall -and -not $ConfigBootstrapped) {
    Install-DefaultConfig
    Install-AuthToken
}

if (-not (Test-Admin)) { Invoke-Elevated }

if ($Uninstall) {
    Write-Host 'Uninstalling krun...'
    Remove-HelperService
    if (Test-Path $InstallDir) { Remove-Item -Recurse -Force $InstallDir }
    Remove-UserPath $InstallDir
    Write-Host 'Uninstall complete.'
    return
}

Write-Host "Installing krun to $InstallDir"
Stop-HelperService
Remove-HelperService
Install-Binaries
Register-HelperService
Add-UserPath $InstallDir
Deploy-TrafficManager

Write-Host ''
Write-Host "Done. Open a new terminal and run: krun version"
