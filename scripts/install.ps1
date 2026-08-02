<#
.SYNOPSIS
depmesh-ai installer for Windows.

.DESCRIPTION
Downloads the release binary, verifies its checksum, installs it, and registers
the MCP server globally (user scope) with every coding CLI and IDE it finds —
so vet_dependency is available in every project, not just the current one.

Run it straight from the release:

    irm https://github.com/jhberges/depmesh-ai/releases/latest/download/install.ps1 | iex

It prompts for the install location and for each client it will touch. To pass
options through a pipe, create the scriptblock explicitly:

    & ([scriptblock]::Create((irm https://github.com/jhberges/depmesh-ai/releases/latest/download/install.ps1))) -Yes -InstallDir C:\tools

.PARAMETER Version
Release tag to install, e.g. v0.3.0. Defaults to the latest release.

.PARAMETER InstallDir
Where to put depmesh-ai.exe. Defaults to %LOCALAPPDATA%\Programs\depmesh.

.PARAMETER Policy
Organization policy file to pin into every client config as DEPMESH_POLICY.

.PARAMETER Clients
Comma-separated subset of claude,copilot,codex,cursor,vscode — or all, or none.

.PARAMETER Yes
Accept every default and never prompt.

.PARAMETER NoConfigure
Install the binary only; do not touch any client configuration.

.PARAMETER Telemetry
Enable telemetry without asking. Off unless you ask for it.

.PARAMETER NoTelemetry
Disable telemetry without asking, removing any previously recorded consent.

.PARAMETER TelemetryUrl
Receiver endpoint. Defaults to https://depmesh.com/v1/telemetry.

.PARAMETER TelemetryToken
Per-tenant ingest key for the receiver ($env:DEPMESH_TELEMETRY_TOKEN).
#>
[CmdletBinding()]
param(
	[string]$Version,
	[string]$InstallDir,
	[string]$Policy,
	[string]$Clients,
	[switch]$Yes,
	[switch]$NoConfigure,
	[switch]$Telemetry,
	[switch]$NoTelemetry,
	[string]$TelemetryUrl,
	[string]$TelemetryToken
)

$ErrorActionPreference = 'Stop'

$Repo = 'jhberges/depmesh-ai'
$BinName = 'depmesh-ai.exe'
$ServerName = 'depmesh'

if (-not $Version)    { $Version = $env:DEPMESH_VERSION }
if (-not $InstallDir) { $InstallDir = $env:DEPMESH_INSTALL_DIR }
if (-not $Policy)     { $Policy = $env:DEPMESH_POLICY }
if (-not $Clients)    { $Clients = $env:DEPMESH_CLIENTS }
if (-not $Version)    { $Version = 'latest' }

# --- telemetry consent settings ---------------------------------------------

if (-not $TelemetryUrl)   { $TelemetryUrl = $env:DEPMESH_TELEMETRY_URL }
if (-not $TelemetryToken) { $TelemetryToken = $env:DEPMESH_TELEMETRY_TOKEN }
$DefaultTelemetryUrl = 'https://depmesh.com/v1/telemetry'

# Must match telemetry.ConfigPath() in the binary. That uses os.UserHomeDir(),
# which is %USERPROFILE% on Windows — so the consent file lives under
# ~\.config, NOT %APPDATA%. Deliberately the same relative path on every
# platform, so installer and binary can never disagree about where it is.
$ConsentFile = if ($env:XDG_CONFIG_HOME) {
	Join-Path $env:XDG_CONFIG_HOME 'depmesh\telemetry.json'
} else {
	Join-Path $env:USERPROFILE '.config\depmesh\telemetry.json'
}

# ask | yes | no — resolved to 'skip' later when there is nobody to ask.
$telemetryMode = 'ask'
if ($Telemetry -or $TelemetryUrl -or $TelemetryToken) { $telemetryMode = 'yes' }
if ($NoTelemetry) { $telemetryMode = 'no' }
if ($Telemetry -and $NoTelemetry) {
	Write-Host 'error: -Telemetry and -NoTelemetry are mutually exclusive' -ForegroundColor Red; exit 1
}

# Private repo, or an internal mirror of the release assets.
$Token = if ($env:DEPMESH_GITHUB_TOKEN) { $env:DEPMESH_GITHUB_TOKEN } else { $env:GITHUB_TOKEN }
$DownloadBase = $env:DEPMESH_DOWNLOAD_BASE

# --- output helpers ---------------------------------------------------------

function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Blue }
function Write-Warn($msg) { Write-Host "warning: $msg" -ForegroundColor Yellow }
function Stop-Install($msg) { Write-Host "error: $msg" -ForegroundColor Red; exit 1 }

$Interactive = (-not $Yes) -and -not [Console]::IsInputRedirected

function Read-Value($prompt, $default) {
	if (-not $Interactive) { return $default }
	$reply = Read-Host "$prompt [$default]"
	if ([string]::IsNullOrWhiteSpace($reply)) { return $default }
	return $reply.Trim()
}

function Read-Confirm($prompt, $default) {
	if (-not $Interactive) { return $default -eq 'y' }
	$hint = if ($default -eq 'y') { 'Y/n' } else { 'y/N' }
	$reply = Read-Host "$prompt [$hint]"
	if ([string]::IsNullOrWhiteSpace($reply)) { $reply = $default }
	return $reply.Trim().ToLower().StartsWith('y')
}

# --- prerequisites ----------------------------------------------------------

Write-Step 'checking prerequisites'

# $IsWindows only exists on PowerShell 6+; on 5.1 the platform is Windows.
$onWindows = if ($null -eq $IsWindows) { $true } else { $IsWindows }
if (-not $onWindows) {
	Stop-Install "this is the Windows installer; on Linux/macOS run:`n  curl -fsSL https://github.com/$Repo/releases/latest/download/install.sh | bash"
}
if ($PSVersionTable.PSVersion.Major -lt 5) {
	Stop-Install "PowerShell 5.1 or newer is required (found $($PSVersionTable.PSVersion))"
}

# Only windows/amd64 is built; ARM64 Windows runs it under emulation.
$arch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 }
switch ($arch) {
	'AMD64' { }
	'ARM64' { Write-Warn 'no native arm64 build — installing the amd64 binary, which Windows runs under emulation' }
	default { Stop-Install "unsupported CPU architecture: $arch (built: amd64)" }
}

# PowerShell 5.1 still defaults to TLS 1.0, which github.com rejects.
try {
	[Net.ServicePointManager]::SecurityProtocol =
		[Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch { }

Write-Step "platform: windows/amd64 (PowerShell $($PSVersionTable.PSVersion))"

function Invoke-Download($url, $outFile, $accept) {
	$headers = @{}
	if ($Token) { $headers['Authorization'] = "Bearer $Token" }
	if ($accept) { $headers['Accept'] = $accept }
	$params = @{ Uri = $url; UseBasicParsing = $true; Headers = $headers }
	if ($outFile) { $params['OutFile'] = $outFile }
	# A visible progress bar makes Invoke-WebRequest an order of magnitude slower.
	$previous = $ProgressPreference
	$ProgressPreference = 'SilentlyContinue'
	try { return Invoke-WebRequest @params } finally { $ProgressPreference = $previous }
}

# --- resolve the release version -------------------------------------------

$release = $null
if ($DownloadBase) {
	if ($Version -eq 'latest') {
		Stop-Install '$DEPMESH_DOWNLOAD_BASE cannot resolve "latest" — pass -Version vX.Y.Z'
	}
} elseif ($Version -eq 'latest' -or $Token) {
	$api = if ($Version -eq 'latest') {
		Write-Step 'resolving latest release'
		"https://api.github.com/repos/$Repo/releases/latest"
	} else {
		"https://api.github.com/repos/$Repo/releases/tags/$Version"
	}
	try {
		$release = (Invoke-Download $api $null 'application/vnd.github+json').Content | ConvertFrom-Json
	} catch {
		if ($Version -eq 'latest') {
			Stop-Install "could not reach the GitHub API: $($_.Exception.Message)`n  retry with -Version vX.Y.Z$(if (-not $Token) { ', or set $GITHUB_TOKEN if the repository is private' })"
		}
		Write-Warn "release metadata unavailable, falling back to the public asset URL"
	}
	if ($Version -eq 'latest') {
		$Version = $release.tag_name
		if (-not $Version) { Stop-Install 'could not determine the latest release; retry with -Version vX.Y.Z' }
	}
}
if ($Version -notmatch '^v') { $Version = "v$Version" }

# --- install location -------------------------------------------------------

if (-not $InstallDir) {
	$InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\depmesh'
	$InstallDir = Read-Value "Install depmesh-ai $Version to which directory?" $InstallDir
}
$InstallDir = [Environment]::ExpandEnvironmentVariables($InstallDir)
if (-not (Test-Path $InstallDir)) {
	try { New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null }
	catch { Stop-Install "cannot create ${InstallDir}: $($_.Exception.Message)" }
}
$InstallDir = (Resolve-Path $InstallDir).Path
$Bin = Join-Path $InstallDir $BinName

# --- download, verify, install ---------------------------------------------

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("depmesh-install-" + [Guid]::NewGuid().ToString('n').Substring(0, 8))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
	$archive = "depmesh_${Version}_windows_amd64.zip"

	# Private repos are only reachable through the API asset endpoint, which
	# serves the file itself only when asked for octet-stream — otherwise it
	# returns the asset's JSON metadata.
	$accept = $null
	if (-not $DownloadBase -and $Token -and $release) { $accept = 'application/octet-stream' }

	function Get-AssetUrl($name) {
		if ($DownloadBase) { return "$($DownloadBase.TrimEnd('/'))/$name" }
		if ($accept) {
			$asset = $release.assets | Where-Object { $_.name -eq $name } | Select-Object -First 1
			if ($asset) { return $asset.url }
			return $null
		}
		return "https://github.com/$Repo/releases/download/$Version/$name"
	}

	Write-Step "downloading $archive"
	$url = Get-AssetUrl $archive
	if (-not $url) { Stop-Install "release $Version has no asset named $archive" }
	$zip = Join-Path $tmp $archive
	try { Invoke-Download $url $zip $accept | Out-Null }
	catch {
		Stop-Install "download failed: $url`n  $($_.Exception.Message)`n  release $Version may not exist, or the repository is private — set `$GITHUB_TOKEN"
	}

	$sumsUrl = Get-AssetUrl 'checksums.txt'
	if ($sumsUrl) {
		try {
			$sums = Join-Path $tmp 'checksums.txt'
			Invoke-Download $sumsUrl $sums $accept | Out-Null
			# checksums.txt comes from `sha256sum ./*`, so names carry a ./ prefix.
			$line = Select-String -Path $sums -Pattern "^([0-9a-f]{64})\s+\.?/?$([regex]::Escape($archive))$" |
				Select-Object -First 1
			if (-not $line) {
				Write-Warn "no checksum published for $archive — skipping verification"
			} else {
				$want = $line.Matches[0].Groups[1].Value
				$got = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLower()
				if ($want -ne $got) {
					Stop-Install "checksum mismatch for $archive`n  expected $want`n  got      $got"
				}
				Write-Step 'checksum verified'
			}
		} catch {
			Write-Warn "could not verify the download: $($_.Exception.Message)"
		}
	}

	Expand-Archive -Path $zip -DestinationPath $tmp -Force
	$extracted = Join-Path $tmp $BinName
	if (-not (Test-Path $extracted)) { Stop-Install "$BinName missing from $archive" }

	Write-Step "installing $Bin"
	try { Copy-Item $extracted $Bin -Force }
	catch { Stop-Install "could not write ${Bin}: $($_.Exception.Message)`n  close any running depmesh-ai, or choose another -InstallDir" }
} finally {
	Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$installed = & $Bin version 2>$null
if (-not $installed) { Stop-Install "$Bin did not run after install" }
Write-Step "installed: $installed"

# --- PATH -------------------------------------------------------------------

# The MCP configs below use the absolute path, so PATH only matters for using
# depmesh-ai from a shell.
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';' | ForEach-Object { $_.TrimEnd('\') }) -notcontains $InstallDir.TrimEnd('\')) {
	if (Read-Confirm "Add $InstallDir to your user PATH?" 'y') {
		$newPath = if ([string]::IsNullOrEmpty($userPath)) { $InstallDir } else { "$userPath;$InstallDir" }
		[Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
		$env:Path = "$env:Path;$InstallDir"
		Write-Step 'PATH updated (new terminals will pick it up)'
	}
}

# --- telemetry consent ------------------------------------------------------

# Asked before client configuration, and deliberately above the -NoConfigure
# early exit: whether this machine shares hallucinated package names is a
# separate decision from which editors get the MCP server registered.

# What is configured now, so the prompt can state it and a re-run never flips
# a previous answer silently.
$currentUrl = ''
if (Test-Path $ConsentFile) {
	try { $currentUrl = (Get-Content -Raw $ConsentFile | ConvertFrom-Json).url } catch { $currentUrl = '' }
}

# Nobody to ask (piped, CI, -Yes) means the question is not answered on the
# user's behalf: leave whatever is already configured exactly as it is.
if ($telemetryMode -eq 'ask' -and -not $Interactive) { $telemetryMode = 'skip' }

if ($telemetryMode -eq 'ask') {
	Write-Host ''
	Write-Host '  Help improve depmesh-ai?'
	Write-Host ''
	Write-Host '  When a package is REJECTed because it does not exist on its registry, that'
	Write-Host '  name is almost always one an AI assistant hallucinated. Sharing those names'
	Write-Host '  builds a live feed of slopsquat targets — so they can be tracked before'
	Write-Host '  attackers register them.'
	Write-Host ''
	Write-Host '    sent      ecosystem, package name, timestamp, tool version'
	Write-Host '    not sent  username, hostname, IP-derived data, repository or project'
	Write-Host '              names — and nothing whatsoever about packages that do exist'
	Write-Host ''
	Write-Host "  This is off unless you say yes, and you can change your mind at any time"
	Write-Host "  (the answer is a single file: $ConsentFile)."
	Write-Host ''
	if ($currentUrl) {
		Write-Host "  Currently: enabled, reporting to $currentUrl"
		$telemetryDefault = 'y'
	} else {
		Write-Host '  Currently: disabled'
		$telemetryDefault = 'n'
	}

	if (Read-Confirm '  Share hallucinated package names?' $telemetryDefault) {
		$telemetryMode = 'yes'
		$suggested = if ($TelemetryUrl) { $TelemetryUrl }
		             elseif ($currentUrl) { $currentUrl }
		             else { $DefaultTelemetryUrl }
		$TelemetryUrl = Read-Value '  Send to' $suggested
		if (-not $TelemetryToken) {
			$TelemetryToken = Read-Value '  Ingest key, if a hosted tenant issued you one (blank for none)' ''
		}
	} else {
		$telemetryMode = 'no'
	}
	Write-Host ''
}

switch ($telemetryMode) {
'yes' {
	if (-not $TelemetryUrl) { $TelemetryUrl = $DefaultTelemetryUrl }
	# Validated rather than escaped: the consent file is JSON, and anything
	# that would need an escape is rejected instead of quoted.
	if ($TelemetryUrl -notmatch '^https?://[^\s"\\]+$') {
		Stop-Install "telemetry URL must be a plain http(s) URL, got: $TelemetryUrl"
	}
	if ($TelemetryToken -and $TelemetryToken -notmatch '^[A-Za-z0-9._-]+$') {
		Stop-Install 'telemetry token may only contain letters, digits, dot, underscore and dash'
	}

	New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ConsentFile) | Out-Null
	$consent = [ordered]@{ url = $TelemetryUrl }
	if ($TelemetryToken) { $consent['token'] = $TelemetryToken }
	# -Encoding ascii: the file is read by Go's encoding/json, which rejects a
	# UTF-8 BOM. Windows PowerShell 5.1 writes one by default with utf8.
	$consent | ConvertTo-Json -Compress | Set-Content -Path $ConsentFile -Encoding ascii

	# The file holds an ingest key. Drop inherited ACLs so only this user reads
	# it — the closest Windows equivalent of the chmod 0600 the Unix script does.
	try {
		icacls $ConsentFile /inheritance:r /grant:r "$($env:USERNAME):(R,W)" *>$null
	} catch {
		Write-Warn "could not restrict permissions on $ConsentFile — it contains an ingest key"
	}
	Write-Step "telemetry ON — reporting nonexistent-package names to $TelemetryUrl"
	Write-Host "    turn it off any time: Remove-Item '$ConsentFile'"
}
'no' {
	if (Test-Path $ConsentFile) {
		Remove-Item -Force $ConsentFile
		Write-Step "telemetry OFF — removed $ConsentFile"
	} else {
		Write-Step 'telemetry OFF — nothing is sent anywhere'
	}
}
'skip' {
	if ($currentUrl) {
		Write-Step "telemetry unchanged — still reporting to $currentUrl"
	} else {
		Write-Step 'telemetry OFF (not asked: not interactive). Enable with: -Telemetry'
	}
}
}

if ($NoConfigure) {
	Write-Step 'done (skipped client configuration)'
	exit 0
}

# --- policy file ------------------------------------------------------------

# Without this, policy is discovered from .\depmesh.policy.json relative to
# wherever the client happened to launch the server — right for per-repo
# policy, wrong for one org-wide file.
if (-not $Policy -and $Interactive) {
	if (Read-Confirm 'Pin an organization policy file into the MCP config?' 'n') {
		$Policy = Read-Value '  Policy file path' (Join-Path $env:APPDATA 'depmesh\policy.json')
	}
}
if ($Policy) {
	$Policy = [Environment]::ExpandEnvironmentVariables($Policy)
	if (-not [IO.Path]::IsPathRooted($Policy)) { $Policy = Join-Path (Get-Location).Path $Policy }
	if (-not (Test-Path $Policy)) {
		Write-Warn "policy file does not exist yet: $Policy`n         create it before using the server, or clients will fail to start"
	}
}

# --- config writing ---------------------------------------------------------

function New-ServerEntry($flavor) {
	$entry = [ordered]@{}
	# Copilot calls stdio servers "local"; VS Code calls them "stdio".
	switch ($flavor) {
		'copilot' { $entry['type'] = 'local' }
		'vscode'  { $entry['type'] = 'stdio' }
	}
	$entry['command'] = $Bin
	$entry['args'] = [string[]]@('serve')
	# Omitting `tools` leaves the tool unlisted in a Copilot session.
	if ($flavor -eq 'copilot') { $entry['tools'] = [string[]]@('*') }
	if ($Policy) { $entry['env'] = [ordered]@{ 'DEPMESH_POLICY' = $Policy } }
	return $entry
}

function Merge-McpConfig($path, $rootKey, $name, $entry) {
	$dir = Split-Path -Parent $path
	if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }

	$doc = $null
	if (Test-Path $path) {
		Copy-Item $path "$path.depmesh.bak" -Force
		$text = Get-Content -Raw -Path $path -ErrorAction SilentlyContinue
		if (-not [string]::IsNullOrWhiteSpace($text)) { $doc = $text | ConvertFrom-Json }
	}
	if ($null -eq $doc) { $doc = [pscustomobject]@{} }
	if ($null -eq $doc.$rootKey) {
		$doc | Add-Member -NotePropertyName $rootKey -NotePropertyValue ([pscustomobject]@{}) -Force
	}
	$doc.$rootKey | Add-Member -NotePropertyName $name -NotePropertyValue $entry -Force

	# WriteAllText with an explicit no-BOM encoding: Set-Content -Encoding UTF8
	# emits a BOM on PowerShell 5.1, which trips up strict JSON parsers.
	$json = ($doc | ConvertTo-Json -Depth 20) + "`r`n"
	[IO.File]::WriteAllText($path, $json, (New-Object Text.UTF8Encoding($false)))
}

# --- clients ----------------------------------------------------------------

function Get-Cli($name) { Get-Command $name -ErrorAction SilentlyContinue | Select-Object -First 1 }

$copilotConfig = Join-Path $env:USERPROFILE '.copilot\mcp-config.json'
$cursorConfig  = Join-Path $env:USERPROFILE '.cursor\mcp.json'
$vscodeConfig  = Join-Path $env:APPDATA 'Code\User\mcp.json'

$catalog = [ordered]@{
	claude  = @{ Label = 'Claude Code (user scope)';                 Detect = { [bool](Get-Cli claude) } }
	copilot = @{ Label = 'GitHub Copilot CLI (~\.copilot)';          Detect = { (Get-Cli copilot) -or (Test-Path (Split-Path -Parent $copilotConfig)) } }
	codex   = @{ Label = 'Codex CLI';                                Detect = { [bool](Get-Cli codex) } }
	cursor  = @{ Label = 'Cursor (IDE)';                             Detect = { Test-Path (Split-Path -Parent $cursorConfig) } }
	vscode  = @{ Label = 'VS Code (IDE)';                            Detect = { (Get-Cli code) -or (Test-Path (Split-Path -Parent $vscodeConfig)) } }
}

function Set-ClaudeClient {
	# `mcp add` refuses to overwrite, so drop any previous registration first.
	& claude mcp remove $ServerName --scope user *>$null
	if ($Policy) {
		& claude mcp add $ServerName --scope user -e "DEPMESH_POLICY=$Policy" -- $Bin serve *>$null
	} else {
		& claude mcp add $ServerName --scope user -- $Bin serve *>$null
	}
	if ($LASTEXITCODE -ne 0) { throw "claude mcp add exited $LASTEXITCODE" }
}

function Set-CopilotClient { Merge-McpConfig $copilotConfig 'mcpServers' $ServerName (New-ServerEntry 'copilot') }
function Set-CursorClient  { Merge-McpConfig $cursorConfig  'mcpServers' $ServerName (New-ServerEntry 'generic') }

function Set-CodexClient {
	& codex mcp --help *>$null
	if ($LASTEXITCODE -ne 0) {
		$toml = "[mcp_servers.$ServerName]`ncommand = `"$($Bin -replace '\\','\\')`"`nargs = [`"serve`"]"
		if ($Policy) { $toml += "`nenv = { DEPMESH_POLICY = `"$($Policy -replace '\\','\\')`" }" }
		throw "this codex build has no 'mcp' subcommand — add to ~\.codex\config.toml:`n$toml"
	}
	& codex mcp remove $ServerName *>$null
	& codex mcp add $ServerName -- $Bin serve *>$null
	if ($LASTEXITCODE -ne 0) { throw "codex mcp add exited $LASTEXITCODE" }
	if ($Policy) { Write-Warn 'set DEPMESH_POLICY for codex in ~\.codex\config.toml — its mcp add takes no env' }
}

# VS Code's own `code --add-mcp` takes a JSON argument, and quoting that
# through cmd.exe shims is unreliable; the user-profile file is equivalent.
function Set-VscodeClient { Merge-McpConfig $vscodeConfig 'servers' $ServerName (New-ServerEntry 'vscode') }

$detected = @($catalog.Keys | Where-Object { & $catalog[$_].Detect })
if (-not $detected) {
	Write-Step 'no supported CLI or IDE found — nothing to configure'
	Write-Host ''
	Write-Host 'Register the MCP server manually with any client:'
	Write-Host "  command: $Bin"
	Write-Host '  args:    ["serve"]'
	exit 0
}

$selected = @()
if ($Clients -eq 'none') {
	$selected = @()
} elseif (-not $Clients -or $Clients -eq 'all') {
	if ($Interactive -and $Clients -ne 'all') {
		Write-Step "found: $($detected -join ', ')"
		foreach ($name in $detected) {
			if (Read-Confirm "Register depmesh with $($catalog[$name].Label)?" 'y') { $selected += $name }
		}
	} else {
		$selected = $detected
	}
} else {
	foreach ($name in ($Clients -split ',' | ForEach-Object { $_.Trim().ToLower() })) {
		if (-not $catalog.Contains($name)) { Write-Warn "unknown client: $name" }
		elseif ($detected -notcontains $name) { Write-Warn "$name requested but not found on this machine" }
		else { $selected += $name }
	}
}

$failed = @()
foreach ($name in $selected) {
	Write-Step "configuring $($catalog[$name].Label)"
	try {
		switch ($name) {
			'claude'  { Set-ClaudeClient }
			'copilot' { Set-CopilotClient }
			'codex'   { Set-CodexClient }
			'cursor'  { Set-CursorClient }
			'vscode'  { Set-VscodeClient }
		}
	} catch {
		Write-Warn $_.Exception.Message
		$failed += $name
	}
}

# --- summary ----------------------------------------------------------------

Write-Host ''
Write-Host "depmesh-ai $Version installed." -ForegroundColor Green
Write-Host "  binary:  $Bin"
if ($Policy) { Write-Host "  policy:  $Policy" }
if (Test-Path $ConsentFile) {
	Write-Host "  telemetry: on  ($ConsentFile)"
} else {
	Write-Host '  telemetry: off'
}
if ($selected) { Write-Host "  clients: $($selected -join ', ')" }
if ($failed) { Write-Host "  needs manual setup: $($failed -join ', ')" }
Write-Host ''
Write-Host 'Try it:'
Write-Host '  depmesh-ai vet npm express'
Write-Host '  depmesh-ai vet npm this-package-definitely-does-not-exist-zz9'
Write-Host ''
Write-Host 'Restart your CLIs/IDEs to pick up the new MCP server, then ask the agent'
Write-Host 'to vet a dependency before installing it.'
