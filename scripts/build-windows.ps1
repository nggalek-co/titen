param([string]$Version = $env:TITEN_VERSION)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
if (-not $IsWindows) { throw 'Windows is required' }
if (-not $Version) { $Version = 'dev' }
if ($Version -notmatch '^[A-Za-z0-9._+\-]+$') { throw 'Invalid version' }
$arch = (& go env GOARCH).Trim()
if ($LASTEXITCODE -ne 0 -or $arch -ne 'amd64') { throw 'The Windows package currently targets amd64' }
$env:CGO_ENABLED = '0'
& cmake -S native/windows -B build/windows-audio -A x64
if ($LASTEXITCODE -ne 0) { throw 'Configure capture helper failed' }
& cmake --build build/windows-audio --config Release
if ($LASTEXITCODE -ne 0) { throw 'Build capture helper failed' }
& ctest --test-dir build/windows-audio -C Release --output-on-failure
if ($LASTEXITCODE -ne 0) { throw 'Capture helper self-test failed' }
$package = 'build/windows/Titen'
New-Item -ItemType Directory -Force $package | Out-Null
& go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o "$package/Titen.exe" .
if ($LASTEXITCODE -ne 0) { throw 'Build desktop failed' }
Copy-Item build/windows-audio/Release/titen-audio.exe "$package/titen-audio.exe" -Force
Copy-Item README.md "$package/README.md" -Force
New-Item -ItemType Directory -Force dist | Out-Null
$archive = "Titen-$Version-windows-$arch.zip"
Compress-Archive -Path $package -DestinationPath "dist/$archive" -Force
$hash = (Get-FileHash "dist/$archive" -Algorithm SHA256).Hash.ToLowerInvariant()
[System.IO.File]::WriteAllText((Join-Path (Get-Location) "dist/$archive.sha256"), "$hash  $archive`n")
Write-Output "Packaged dist/$archive"
