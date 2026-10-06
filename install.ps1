# Installs or updates vault on Windows. In PowerShell:
#   irm https://github.com/shelo16/vault/releases/latest/download/install.ps1 | iex
# Installs to %LOCALAPPDATA%\Programs\vault, adds it to your PATH and
# creates a "Vault" Start-menu shortcut that opens the GUI.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = if ($env:VAULT_REPO) { $env:VAULT_REPO } else { 'shelo16/vault' }
$dir  = Join-Path $env:LOCALAPPDATA 'Programs\vault'
New-Item -ItemType Directory -Force -Path $dir | Out-Null

foreach ($f in 'vault.exe', 'vaultw.exe') {
    $url  = "https://github.com/$repo/releases/latest/download/$f"
    $dest = Join-Path $dir $f
    $new  = "$dest.new"
    Write-Host "downloading $f ..."
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $new
    Unblock-File $new
    # a running exe can't be overwritten but can be renamed
    Remove-Item "$dest.old" -Force -ErrorAction SilentlyContinue
    if (Test-Path $dest) { Move-Item $dest "$dest.old" -Force }
    Move-Item $new $dest -Force
    Remove-Item "$dest.old" -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
if (($userPath -split ';') -notcontains $dir) {
    $newPath = (($userPath.TrimEnd(';'), $dir) | Where-Object { $_ }) -join ';'
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Write-Host "added $dir to your PATH — open a new terminal to use 'vault'"
}
$env:Path = "$env:Path;$dir"

$lnk = Join-Path ([Environment]::GetFolderPath('Programs')) 'Vault.lnk'
$sh  = (New-Object -ComObject WScript.Shell).CreateShortcut($lnk)
$sh.TargetPath  = Join-Path $dir 'vaultw.exe'
$sh.Description = 'Open the vault GUI'
$sh.Save()

Write-Host ("installed: " + (& (Join-Path $dir 'vault.exe') version))
Write-Host "Start menu: 'Vault' opens the GUI."
