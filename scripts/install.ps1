# Hermesh installer for Windows. Downloads release programs from GitHub
# Releases, verifies them against the signed checksum list, installs them
# under %LOCALAPPDATA%\Programs\Hermesh\bin and (for a machine) starts
# pairing. Windows PowerShell 5.1 or later; no administrator needed.
#
#   Add a machine:   irm https://github.com/hermeshai/hermesh/releases/latest/download/install.ps1 | iex
#                    (with a connection address or a name:
#                    & ([scriptblock]::Create((irm https://github.com/hermeshai/hermesh/releases/latest/download/install.ps1))) <address> -Name build-box)
#   Agent CLI only:  & ([scriptblock]::Create((irm .../install.ps1))) -Cli
#
# Environment: HERMESH_VERSION (default latest), HERMESH_BIN_DIR (default
# %LOCALAPPDATA%\Programs\Hermesh\bin), HERMESH_NO_PATH=1 (leave the user's
# PATH alone).
param([string]$Address = "", [switch]$Cli, [string]$Name = "")

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue" # Invoke-WebRequest crawls with a progress bar
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$Repo = "hermeshai/hermesh"
$Version = if ($env:HERMESH_VERSION) { $env:HERMESH_VERSION } else { "latest" }
$BinDir = if ($env:HERMESH_BIN_DIR) { $env:HERMESH_BIN_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\Hermesh\bin" }

# Release signing public key (ECDSA P-256). Replaced at release time.
$PubKey = '__HERMESH_RELEASE_PUBKEY__'

switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
  "X64" { $Arch = "amd64" }
  "Arm64" { $Arch = "arm64" }
  default { throw "hermesh install: unsupported architecture $_" }
}
if ($env:HERMESH_DOWNLOAD_BASE) {
  $Base = $env:HERMESH_DOWNLOAD_BASE
} elseif ($Version -eq "latest") {
  $Base = "https://github.com/$Repo/releases/latest/download"
} else {
  $Base = "https://github.com/$Repo/releases/download/$Version"
}

# Test-Signature checks openssl's DER signature of Data by the P-256 key in
# Pem, with what Windows PowerShell 5.1's .NET has (CNG).
function Test-Signature([byte[]]$Data, [byte[]]$Der, [string]$Pem) {
  $spki = [Convert]::FromBase64String(($Pem -replace '-----[^-]+-----', '' -replace '\s', ''))
  # A P-256 SubjectPublicKeyInfo ends with its point: 04, X, Y.
  if ($spki.Length -ne 91 -or $spki[26] -ne 4) { throw "hermesh install: unexpected release key" }
  $blob = New-Object byte[] 72
  [BitConverter]::GetBytes([uint32]0x31534345).CopyTo($blob, 0) # "ECS1": an ECDSA P-256 public key
  [BitConverter]::GetBytes([uint32]32).CopyTo($blob, 4)
  [Array]::Copy($spki, 27, $blob, 8, 64)
  $ecdsa = New-Object Security.Cryptography.ECDsaCng([Security.Cryptography.CngKey]::Import($blob, [Security.Cryptography.CngKeyBlobFormat]::EccPublicBlob))
  $ecdsa.HashAlgorithm = [Security.Cryptography.CngAlgorithm]::Sha256
  # DER is SEQUENCE { INTEGER r, INTEGER s }; CNG wants r and s, 32 bytes each.
  $raw = New-Object byte[] 64
  $i = 2
  foreach ($at in 0, 32) {
    if ($Der[$i] -ne 2) { return $false }
    $len = $Der[$i + 1]
    $v = [byte[]]$Der[($i + 2)..($i + 1 + $len)]
    $skip = 0
    while ($v.Length - $skip -gt 32 -and $v[$skip] -eq 0) { $skip++ }
    if ($v.Length - $skip -gt 32) { return $false }
    [Array]::Copy($v, $skip, $raw, $at + 32 - ($v.Length - $skip), $v.Length - $skip)
    $i += 2 + $len
  }
  return $ecdsa.VerifyData($Data, $raw)
}

$Tmp = Join-Path ([IO.Path]::GetTempPath()) ("hermesh-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $Tmp | Out-Null
try {
  Write-Host "> downloading checksums"
  Invoke-WebRequest -UseBasicParsing "$Base/checksums.txt" -OutFile "$Tmp\checksums.txt"
  Invoke-WebRequest -UseBasicParsing "$Base/checksums.txt.sig" -OutFile "$Tmp\checksums.txt.sig"
  if ($PubKey.StartsWith("__HERMESH")) { throw "hermesh install: this installer has no release signing key; download a released install.ps1" }
  if (-not (Test-Signature ([IO.File]::ReadAllBytes("$Tmp\checksums.txt")) ([IO.File]::ReadAllBytes("$Tmp\checksums.txt.sig")) $PubKey)) {
    throw "hermesh install: checksum signature verification FAILED; refusing to install"
  }
  $sums = @{}
  foreach ($line in Get-Content "$Tmp\checksums.txt") {
    $f = $line -split '\s+'
    if ($f.Length -ge 2) { $sums[$f[1].TrimStart('*')] = $f[0].ToLower() }
  }
  $bins = if ($Cli) { @("hermesh") } else { @("hermeshd", "hermeshdw", "hermesh") }
  New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
  foreach ($b in $bins) {
    $asset = "$b-windows-$Arch.exe"
    Write-Host "> downloading $asset"
    $file = Join-Path $Tmp $asset
    Invoke-WebRequest -UseBasicParsing "$Base/$asset" -OutFile $file
    if (-not $sums.ContainsKey($asset)) { throw "hermesh install: $asset is not in the signed checksum list" }
    if ((Get-FileHash -Algorithm SHA256 $file).Hash.ToLower() -ne $sums[$asset]) { throw "hermesh install: $asset checksum mismatch" }
    # A running program can be renamed, not overwritten: the old one moves
    # aside, and goes once nothing runs it.
    $dest = Join-Path $BinDir "$b.exe"
    Get-ChildItem -Path $BinDir -Filter "$b.exe.*.old" -ErrorAction SilentlyContinue | Remove-Item -Force -ErrorAction SilentlyContinue
    if (Test-Path $dest) { Move-Item $dest "$dest.$(Get-Random).old" }
    Move-Item $file $dest
  }
  Write-Host "installed to $BinDir"
} finally {
  Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}

# The user's PATH, kept as written (its %VARIABLES% unexpanded).
$envKey = Get-Item -Path "HKCU:\Environment"
$userPath = $envKey.GetValue("Path", "", [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
if (-not $env:HERMESH_NO_PATH -and -not (($userPath -split ';') -contains $BinDir)) {
  Set-ItemProperty -Path "HKCU:\Environment" -Name Path -Type ExpandString -Value ((($userPath.TrimEnd(';')) + ";" + $BinDir).TrimStart(';'))
  # Setting any user variable tells running programs the environment changed.
  [Environment]::SetEnvironmentVariable("HERMESH_INSTALLED", "1", "User")
  [Environment]::SetEnvironmentVariable("HERMESH_INSTALLED", $null, "User")
  Write-Host "added $BinDir to your PATH (new terminals see it)"
}
if (-not (($env:Path -split ';') -contains $BinDir)) { $env:Path = "$env:Path;$BinDir" }

if ($Cli) {
  Write-Host "Next: hermesh auth login   (the official center; or give a connection address)"
  return
}
$pair = @("pair")
if ($Name) { $pair += @("--name", $Name) }
if ($Address) { $pair += $Address }
& (Join-Path $BinDir "hermeshd.exe") @pair
