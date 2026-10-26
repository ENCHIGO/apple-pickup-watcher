# Install the apw CLI from the official GitHub Releases of ENCHIGO/apple-pickup-watcher.
#
#   powershell -ExecutionPolicy Bypass -File install-apw.ps1
#   $env:APW_VERSION = "v0.7.0"; powershell -ExecutionPolicy Bypass -File install-apw.ps1
#
# The archive is checked against the .sha256 file published next to it; any
# mismatch or download failure aborts without installing anything.
# Keep this file ASCII-only: Windows PowerShell 5.1 reads BOM-less UTF-8 as ANSI.
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Repo = "ENCHIGO/apple-pickup-watcher"
$Base = "https://github.com/$Repo/releases"
$Dest = if ($env:APW_INSTALL_DIR) { $env:APW_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "apw\bin" }

if (-not [Environment]::Is64BitOperatingSystem) { throw "install-apw: prebuilt packages exist only for 64-bit Windows" }
$Target = "x86_64-pc-windows-msvc"

$Tag = $env:APW_VERSION
if (-not $Tag) {
    # Follow the releases/latest redirect instead of the REST API: no rate limit, no token.
    $Request = [System.Net.WebRequest]::Create("$Base/latest")
    $Request.AllowAutoRedirect = $true
    $Response = $Request.GetResponse()
    $Tag = $Response.ResponseUri.Segments[-1]
    $Response.Close()
}
if (-not $Tag.StartsWith("v")) { $Tag = "v$Tag" }
if ($Tag -notmatch '^v\d+\.\d+\.\d+') { throw "install-apw: could not determine the release version (got '$Tag')" }

$Name = "apw-cli-$Tag-$Target"
$Tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("apw-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $Tmp | Out-Null
try {
    Write-Host "Downloading $Name.zip ..."
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/download/$Tag/$Name.zip" -OutFile "$Tmp\$Name.zip"
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/download/$Tag/$Name.zip.sha256" -OutFile "$Tmp\$Name.zip.sha256"

    $Expected = ((Get-Content "$Tmp\$Name.zip.sha256" -Raw).Trim() -split '\s+')[0].ToLower()
    $Actual = (Get-FileHash -Algorithm SHA256 "$Tmp\$Name.zip").Hash.ToLower()
    if (-not $Expected -or $Expected -ne $Actual) { throw "install-apw: sha256 mismatch (expected $Expected, got $Actual); nothing installed" }

    Expand-Archive -Path "$Tmp\$Name.zip" -DestinationPath $Tmp
    $Exe = Join-Path $Tmp "$Name\apw.exe"
    if (-not (Test-Path $Exe)) { throw "install-apw: apw.exe not found in the archive" }
    New-Item -ItemType Directory -Force -Path $Dest | Out-Null
    Copy-Item -Force $Exe (Join-Path $Dest "apw.exe")
} finally {
    Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}

$Installed = Join-Path $Dest "apw.exe"
& $Installed --version
if ($LASTEXITCODE -ne 0) { throw "install-apw: installed to $Installed but it does not run" }
Write-Host "Installed: $Installed"
if (-not (($env:PATH -split ';') -contains $Dest)) {
    Write-Host "Note: $Dest is not on PATH; use the full path $Installed"
}
Write-Output $Installed
