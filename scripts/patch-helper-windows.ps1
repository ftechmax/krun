$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot

try {
    $outputPath = Join-Path $repoRoot 'krun-helper.exe'
    $manifestPath = Join-Path $repoRoot 'cmd\krun-helper\krun-helper.manifest'

    if (-not (Test-Path $outputPath)) {
        throw "Helper binary not found at '$outputPath'. Build it before applying the UAC patch."
    }

    if (-not (Test-Path $manifestPath)) {
        throw "Manifest not found at '$manifestPath'."
    }

    $mtPath = $null
    $localMt = Join-Path $PSScriptRoot 'mt.exe'
    if (Test-Path $localMt) {
        $mtPath = $localMt
    }

    $whereMt = Get-Command mt.exe -ErrorAction SilentlyContinue
    if (-not $mtPath -and $whereMt) {
        $mtPath = $whereMt.Path
    }

    if (-not $mtPath) {
        $kitRoot = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin'
        if (Test-Path $kitRoot) {
            $versioned = Get-ChildItem -Path $kitRoot -Directory -ErrorAction SilentlyContinue |
                ForEach-Object { Join-Path $_.FullName 'x64\mt.exe' } |
                Where-Object { Test-Path $_ } |
                Sort-Object -Descending

            if ($versioned.Count -gt 0) {
                $mtPath = $versioned[0]
            }
        }
    }

    if (-not $mtPath) {
        $fallback = Join-Path ${env:ProgramFiles(x86)} 'Windows Kits\10\bin\x64\mt.exe'
        if (Test-Path $fallback) {
            $mtPath = $fallback
        }
    }

    if (-not $mtPath) {
        throw 'mt.exe not found. Install Windows SDK Build Tools and retry.'
    }

    & $mtPath -manifest $manifestPath -outputresource:$outputPath`;1

    Write-Host "Patched $outputPath"
}
finally {
    Pop-Location
}
