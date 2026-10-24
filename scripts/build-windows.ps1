param(
    [string]$Version = $env:TITEN_VERSION
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
if (-not $IsWindows) { throw 'Windows is required to build the WASAPI helper' }
if (-not $Version) { $Version = 'dev' }
if ($Version -notmatch '^[A-Za-z0-9._+\-]+$') { throw 'Invalid version' }
if ($Version -match '^v' -and -not $env:MYGO_UPDATER_PRIVATE_KEY) { throw 'Tagged Windows builds require MYGO_UPDATER_PRIVATE_KEY to sign automatic updates' }
$mygoVersion = if ($Version -match '^v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)$') { $Matches[1] } else { '0.0.0' }
$arch = (& go env GOARCH).Trim()
if ($LASTEXITCODE -ne 0 -or $arch -ne 'amd64') { throw 'The Windows package currently targets amd64' }
$env:CGO_ENABLED = '0'

& cmake -S native/windows -B build/windows-audio -A x64
if ($LASTEXITCODE -ne 0) { throw 'Configure capture helper failed' }
& cmake --build build/windows-audio --config Release
if ($LASTEXITCODE -ne 0) { throw 'Build capture helper failed' }
& ctest --test-dir build/windows-audio -C Release --output-on-failure
if ($LASTEXITCODE -ne 0) { throw 'Capture helper self-test failed' }

$root = (Get-Location).Path
$resourceDir = Join-Path $root 'resources/windows-amd64'
$resourcePath = Join-Path $resourceDir 'titen-audio.exe'
$configPath = Join-Path $root 'mygo.json'
$originalConfig = [System.IO.File]::ReadAllBytes($configPath)
New-Item -ItemType Directory -Force $resourceDir | Out-Null
Copy-Item 'build/windows-audio/Release/titen-audio.exe' $resourcePath -Force
try {
    $config = [System.IO.File]::ReadAllText($configPath) | ConvertFrom-Json
    $config.version = $mygoVersion
    $configJson = $config | ConvertTo-Json -Depth 16
    [System.IO.File]::WriteAllText($configPath, $configJson + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))

    $arguments = @('run', 'github.com/egoist/mygo/cmd/mygo', 'build')
    if ($Version -notmatch '^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') { $arguments += '-debug' }
    $arguments += @('-platform', 'windows/amd64', '-o', 'dist')
    & go @arguments
    if ($LASTEXITCODE -ne 0) { throw 'MyGo Windows packaging failed' }

    $package = 'build/windows/Titen'
    New-Item -ItemType Directory -Force $package | Out-Null
    Copy-Item 'dist/windows-amd64/Titen.exe' "$package/Titen.exe" -Force
    Copy-Item 'dist/windows-amd64/titen-audio.exe' "$package/titen-audio.exe" -Force
    Copy-Item README.md "$package/README.md" -Force
    Get-ChildItem dist -File -Filter 'Titen-*-windows-amd64.zip' | Remove-Item -Force
    Get-ChildItem dist -File -Filter 'Titen-*-windows-amd64.zip.sha256' | Remove-Item -Force
    $archive = "Titen-$Version-windows-amd64.zip"
    Compress-Archive -Path $package -DestinationPath "dist/$archive" -Force
    $hash = (Get-FileHash "dist/$archive" -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText((Join-Path $root "dist/$archive.sha256"), "$hash  $archive`n")
    $installers = @(Get-ChildItem 'dist/windows-amd64' -File -Filter "Titen Setup $mygoVersion amd64.exe")
    if ($installers.Count -ne 1) { throw 'MyGo did not produce the per-user Windows installer' }
    $installerHash = (Get-FileHash $installers[0].FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText("$($installers[0].FullName).sha256", "$installerHash  $($installers[0].Name)`n")
    if ($env:MYGO_UPDATER_PRIVATE_KEY) {
        if (-not (Test-Path 'dist/windows-amd64/update-windows-amd64.json') -or
            -not (Test-Path "dist/windows-amd64/Titen-$mygoVersion-windows-amd64.tar.gz")) {
            throw 'MyGo did not produce the signed Windows auto-update manifest and archive'
        }
    }
    Write-Output "Packaged dist/$archive and MyGo installer/update assets"
}
finally {
    [System.IO.File]::WriteAllBytes($configPath, $originalConfig)
    Remove-Item $resourcePath -Force -ErrorAction SilentlyContinue
    if ((Test-Path $resourceDir) -and -not (Get-ChildItem $resourceDir -Force | Select-Object -First 1)) {
        Remove-Item $resourceDir -Force
    }
}
