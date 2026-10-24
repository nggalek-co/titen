$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
if (-not $IsWindows) { throw 'Windows is required' }
$sandbox = Join-Path $env:RUNNER_TEMP "titen-upgrade-$PID"
$installDir = Join-Path $sandbox 'Programs/Titen'
$env:APPDATA = Join-Path $sandbox 'AppData'
$env:LOCALAPPDATA = Join-Path $sandbox 'LocalAppData'
New-Item -ItemType Directory -Force $installDir, $env:APPDATA, $env:LOCALAPPDATA | Out-Null

function Install-Titen([string]$version) {
    & ./scripts/build-windows.ps1 -Version $version
    if ($LASTEXITCODE -ne 0) { throw "Build $version failed" }
    $installer = "dist/windows-amd64/Titen Setup $version amd64.exe"
    if (-not (Test-Path $installer)) { throw "Installer $installer is missing" }
    $savedInstaller = Join-Path $sandbox "Titen-$version-setup.exe"
    Copy-Item $installer $savedInstaller -Force
    $process = Start-Process -FilePath $savedInstaller -ArgumentList @('/S', "/D=$installDir") -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "Installer $version exited with $($process.ExitCode)" }
    $exe = Join-Path $installDir 'Titen.exe'
    if (-not (Test-Path $exe)) { throw "Titen.exe was not installed for $version" }
    $installedVersion = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($exe).ProductVersion
    if (-not $installedVersion.StartsWith($version, [StringComparison]::Ordinal)) {
        throw "Expected version $version after install, got $installedVersion"
    }
    $registration = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\co.nggalek.titen'
    if ($registration.DisplayVersion -ne $version) { throw "Installed version registration is $($registration.DisplayVersion), expected $version" }
}

try {
    Install-Titen '0.0.1'
    $settingsDir = Join-Path $env:APPDATA 'Titen'
    New-Item -ItemType Directory -Force $settingsDir | Out-Null
    $settingsPath = Join-Path $settingsDir 'settings.json'
    [System.IO.File]::WriteAllText($settingsPath, '{"microphone_id":"upgrade-persistence-marker","capture_mode":"Both","language":"id"}')
    $settingsHash = (Get-FileHash $settingsPath -Algorithm SHA256).Hash
    Install-Titen '0.0.2'
    if (-not (Test-Path $settingsPath) -or (Get-FileHash $settingsPath -Algorithm SHA256).Hash -ne $settingsHash) {
        throw 'The per-user Titen settings changed during the installer upgrade'
    }
    Write-Output 'Verified per-user installation upgrade 0.0.1 -> 0.0.2 and preserved %APPDATA%\Titen\settings.json'
}
finally {
    Remove-Item $sandbox -Recurse -Force -ErrorAction SilentlyContinue
}
