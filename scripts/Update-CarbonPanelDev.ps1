# SPDX-License-Identifier: MPL-2.0
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at https://mozilla.org/MPL/2.0/.

<#
.SYNOPSIS
  Scoped dev-nightly updater for the existing local WSL Carbon Panel install.

.DESCRIPTION
  Production scope is deliberately fixed: Compose project carbon-panel-host,
  service carbon-panel, loopback http://127.0.0.1:8095, Docker relay
  tcp://[::1]:2375. There are NO target overrides in Production scope. The
  script reads the trusted registry and GitHub main/CI revision, resolves the
  immutable digest, refuses wrong/stale/downward targets, and only then pulls
  and recreates the single scoped service with `compose up -d --no-deps`.

  DEFAULT IS DRY-RUN. Pass -Apply to mutate anything. Before any write it
  takes a durable lock, snapshots the controller SQLite with the online backup
  API, preserves a rollback image, and after recreation verifies health,
  mounts, image identity and data. On verification failure it rolls back to
  the preserved image and restores the database if needed.

  No elevation, no secrets, no registry/release/deployment writes, no other
  container, WSL service or game server is touched. Worlds and bind mounts are
  never deleted. Fixture scope is for task-owned qualification only; it uses a
  local manifest instead of live services and requires a fixture marker label.

.PARAMETER Apply
  Perform the update. Without it the script only prints the decision and plan.

.PARAMETER Scope
  Production (default, fixed target) or Fixture (task-owned qualification).

.PARAMETER FixtureManifest
  Required JSON manifest in Fixture scope. Must declare "fixture": true.

.PARAMETER HealthTimeoutSeconds
  Bounded readiness timeout in seconds (default 90).

.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File Update-CarbonPanelDev.ps1
.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File Update-CarbonPanelDev.ps1 -Apply
.EXAMPLE
  powershell -NoProfile -ExecutionPolicy Bypass -File Update-CarbonPanelDev.ps1 -Scope Fixture -FixtureManifest .\fixture\manifest-target.json -Apply
#>
[CmdletBinding()]
param(
  [switch]$Apply,
  [ValidateSet('Production', 'Fixture')]
  [string]$Scope = 'Production',
  [string]$FixtureManifest,
  [switch]$SimulateDataFailure,
  [switch]$FixtureCorruptDatabase,
  [switch]$FixtureCommitMigration,
  [switch]$FixtureForceImageFailure,
  [ValidateRange(5, 600)]
  [int]$HealthTimeoutSeconds = 90
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# --- Fixed, audited scope ---------------------------------------------------
$DockerHost = 'tcp://[::1]:2375'
$ProdProject = 'carbon-panel-host'
$ProdService = 'carbon-panel'
$ProdPort = 8095
# The Windows Docker CLI reads the compose file locally; the daemon resolves
# its /opt/... bind sources inside WSL. No target override is accepted.
$ProdCompose = '\\wsl$\ubuntu_general\opt\carbon-panel-host\docker-compose.yml'
$ProdDataDir = '/opt/carbon-panel-host/data'
$ProdBackupsDir = '/opt/carbon-panel-host/backups'
$RegistryImage = 'atharvnaphade/carbonpanel'
$GitHubRepo = 'athNdev/carbonpanel'
$RequiredArchitectures = @('linux/amd64', 'linux/arm64')
$RollbackTagPrefix = 'carbon-panel:devnightly-rollback-'
$MaxRollbackTags = 3
$WslDistro = 'ubuntu_general'

function Write-Step {
  param([string]$Message)
  Write-Host "[dev-nightly] $Message"
}

function Fail {
  param([string]$Message)
  throw $Message
}

function Invoke-Native {
  param(
    [Parameter(Mandatory = $true)][string]$File,
    [string[]]$Arguments = @(),
    [switch]$AllowFailure
  )
  # Native stderr must not be promoted to a terminating ErrorRecord; the exit
  # code is the authority and benign relay/WSL warnings are filtered by callers.
  $previous = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  try {
    $output = & $File @Arguments 2>&1
    $code = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previous
  }
  if ($code -ne 0 -and -not $AllowFailure) {
    Fail ("command failed ({0}): {1} {2} :: {3}" -f $code, $File, ($Arguments -join ' '), ($output -join ' '))
  }
  return , ($output | ForEach-Object { $_.ToString() })
}

function Invoke-Docker {
  param([string[]]$Arguments = @(), [switch]$AllowFailure)
  return Invoke-Native -File 'docker' -Arguments (@('--host', $DockerHost) + $Arguments) -AllowFailure:$AllowFailure
}

function Invoke-DockerJson {
  param([string[]]$Arguments = @())
  $raw = Invoke-Docker -Arguments $Arguments
  $text = ($raw -join "`n").Trim()
  if (-not $text) { Fail 'empty docker inspect output' }
  return ($text | ConvertFrom-Json)
}

function Invoke-WslPython {
  param(
    [Parameter(Mandatory = $true)][string]$Code,
    [string[]]$Args = @()
  )
  # The known F: automount warning is benign on this host; other stderr is kept.
  $previous = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  try {
    $all = $Code | & wsl.exe -d $WslDistro -- python3 - @Args 2>&1
    $codeExit = $LASTEXITCODE
  } finally {
    $ErrorActionPreference = $previous
  }
  if ($codeExit -ne 0) { Fail ("wsl python failed ({0}): {1}" -f $codeExit, ($all -join ' ')) }
  return , ($all | Where-Object { $_.ToString() -notmatch 'Failed to mount F:' } | ForEach-Object { $_.ToString() })
}

function Invoke-Gh {
  param([string[]]$Arguments = @(), [switch]$AllowFailure)
  return Invoke-Native -File 'gh' -Arguments $Arguments -AllowFailure:$AllowFailure
}

function Get-LockDir {
  $base = $env:LOCALAPPDATA
  if (-not $base) { $base = $env:TEMP }
  if (-not $base) { Fail 'no writable local application data directory' }
  return Join-Path $base 'carbonpanel-devnightly'
}

# --- Target resolution ------------------------------------------------------
function Get-ManifestTarget {
  param([Parameter(Mandatory = $true)][string]$Path)
  if (-not (Test-Path -LiteralPath $Path)) { Fail "fixture manifest not found: $Path" }
  $raw = Get-Content -LiteralPath $Path -Raw
  $m = $raw | ConvertFrom-Json
  if (-not $m.fixture -or $m.fixture -ne $true) { Fail 'fixture manifest must declare fixture=true' }
  if (-not (($m.project -is [string]) -and $m.project.StartsWith('carbonpanel-devnightly-'))) {
    Fail 'fixture manifest project must be a task-owned carbonpanel-devnightly-* project'
  }
  if (-not (($m.expected_mounts) -and $m.expected_mounts.Count -gt 0)) {
    Fail 'fixture manifest must declare expected mounts'
  }
  foreach ($name in @('target_digest', 'platform_digest', 'config_digest')) {
    if (-not (($m.$name -is [string]) -and $m.$name -match '^sha256:[0-9a-f]{64}$')) {
      Fail "fixture manifest $name must be a full sha256 digest"
    }
  }
  if ($null -eq $m.local_forward_ok) { Fail 'fixture manifest must declare local_forward_ok' }
  foreach ($name in @('target_commit', 'current_commit')) {
    if (-not (($m.$name -is [string]) -and $m.$name -match '^[0-9a-f]{40}$')) {
      Fail "fixture manifest $name must be a full 40-hex commit"
    }
  }
  return $m
}

function Get-LocalPlatform {
  # The daemon's platform determines which child manifest `pull repo@index`
  # selects. Only these two Linux architectures are supported by this updater.
  $arch = ([string]((Invoke-Docker -Arguments @('info', '--format', '{{.Architecture}}')) -join '')).Trim()
  switch ($arch) {
    'x86_64' { return 'linux/amd64' }
    'amd64' { return 'linux/amd64' }
    'aarch64' { return 'linux/arm64' }
    'arm64' { return 'linux/arm64' }
    default { Fail "unsupported daemon architecture: $arch" }
  }
}

function Get-RegistryIdentity {
  param([Parameter(Mandatory = $true)][string]$Reference)
  # Three DIFFERENT digest objects are kept distinct:
  #   IndexDigest  - multi-platform OCI index (the registry `repo@sha256:` pin)
  #   child Digest - per-platform manifest descriptor
  #   ConfigDigest - the image config object; this is what Docker reports as
  #                  `image inspect .Id` and `container inspect .Image`.
  $indexRaw = (Invoke-Docker -Arguments @('buildx', 'imagetools', 'inspect',
      $Reference, '--format', '{{json .Manifest}}')) -join "`n"
  $index = $indexRaw | ConvertFrom-Json
  $indexDigest = [string]$index.digest
  # A digest-anchored reference already contains '@'; child manifests must be
  # addressed from the bare repository, never by appending a second '@'.
  $baseRef = ($Reference -split '@', 2)[0]
  if ($baseRef -match '^(.*):[^/]+$') { $baseRef = $Matches[1] }
  $platforms = @{}
  foreach ($child in @($index.manifests)) {
    $key = "$([string]$child.platform.os)/$([string]$child.platform.architecture)"
    if ($RequiredArchitectures -notcontains $key) { continue }
    $childDigest = [string]$child.digest
    if (-not $childDigest) { continue }
    $childRaw = (Invoke-Docker -Arguments @('buildx', 'imagetools', 'inspect',
        "$baseRef@$childDigest", '--format', '{{json .Image}}')) -join "`n"
    $image = $childRaw | ConvertFrom-Json
    $labels = $image.config.Labels
    $revision = ''
    if ($labels) {
      $revision = [string]$labels.'org.opencontainers.image.revision'
      if (-not $revision) { $revision = [string]$labels.revision }
    }
    # The raw child manifest carries the config digest; buildx's formatted
    # output does not expose it, so read the manifest descriptor directly.
    $manifestRaw = (Invoke-Docker -Arguments @('manifest', 'inspect',
        "$baseRef@$childDigest")) -join "`n"
    $manifest = $manifestRaw | ConvertFrom-Json
    $configDigest = [string]$manifest.config.digest
    $platforms[$key] = [pscustomobject]@{
      Digest = $childDigest
      ConfigDigest = $configDigest
      Revision = $revision
    }
  }
  return [pscustomobject]@{ IndexDigest = $indexDigest; Platforms = $platforms }
}

function Resolve-TrustedTarget {
  param([Parameter(Mandatory = $true)]$Config)

  if ($Config.Scope -eq 'Fixture') {
    $m = Get-ManifestTarget -Path $Config.FixtureManifest
    return [pscustomobject]@{
      Project = $m.project
      Service = $m.service
      Endpoint = $m.endpoint
      Compose = $m.compose
      DataDir = $m.data_dir
      BackupsDir = $m.backups_dir
      ExpectedMounts = @($m.expected_mounts)
      CurrentCommit = $m.current_commit
      Commit = $m.target_commit
      Digest = $m.target_digest
      PreviousCommit = [string]$m.previous_commit
      PreviousDigest = [string]$m.previous_digest
      CiVerified = [bool]$m.ci_success
      CompareStatus = [string]$m.compare_status
      Architectures = @($m.architectures)
      RevisionMatches = [bool]$m.revision_matches
      IndexDigest = [string]$m.target_digest
      PlatformDigest = [string]$m.platform_digest
      ConfigDigest = [string]$m.config_digest
      Platform = 'linux/amd64'
      MainIdentical = $true
      LocalForwardOk = [bool]($m.local_forward_ok)
      CommitOrder = @($m.commit_order)
      LocalRef = [string]$m.target_ref
      SkipPull = $true
      Fixture = $true
    }
  }

  # Production: GitHub main + exact-commit CI, then the trusted registry.
  $mainSha = (Invoke-Gh -Arguments @('api', "repos/$GitHubRepo/commits/main", '--jq', '.sha')) -join ''
  $mainSha = $mainSha.Trim()
  if ($mainSha -notmatch '^[0-9a-f]{40}$') { Fail 'could not resolve a full main SHA from GitHub' }

  $aliasRaw = (Invoke-Docker -Arguments @('buildx', 'imagetools', 'inspect',
      "$RegistryImage`:nightly", '--format', '{{json .Manifest}}')) -join "`n"
  $aliasIndex = $aliasRaw | ConvertFrom-Json
  $aliasDigest = [string]$aliasIndex.digest
  if ($aliasDigest -notmatch '^sha256:[0-9a-f]{64}$') { Fail 'nightly alias has no immutable index digest' }

  $aliasIdentity = Get-RegistryIdentity -Reference "$RegistryImage@$aliasDigest"
  foreach ($arch in $RequiredArchitectures) {
    if (-not $aliasIdentity.Platforms.ContainsKey($arch)) {
      Fail "nightly image is missing required architecture $arch"
    }
    $entry = $aliasIdentity.Platforms[$arch]
    if ($entry.ConfigDigest -notmatch '^sha256:[0-9a-f]{64}$') {
      Fail "nightly image has no config digest for $arch"
    }
  }
  $commits = @()
  foreach ($arch in $RequiredArchitectures) { $commits += $aliasIdentity.Platforms[$arch].Revision }
  $commit = ($commits | Select-Object -Unique)
  if (@($commit).Count -ne 1 -or [string]$commit -notmatch '^[0-9a-f]{40}$') {
    Fail 'nightly platform configs do not agree on one full commit revision'
  }
  $commit = [string]$commit

  # Immutable provenance: sha-<commit> must be the SAME index, child manifests
  # and image configs. A config ID is NOT an index digest and is never compared
  # against one.
  $immutableRef = "$RegistryImage`:sha-$commit"
  $immutableIdentity = Get-RegistryIdentity -Reference $immutableRef
  if ($immutableIdentity.IndexDigest -ne $aliasDigest) {
    Fail 'nightly index digest differs from the verified sha-<commit> image'
  }
  foreach ($arch in $RequiredArchitectures) {
    $a = $aliasIdentity.Platforms[$arch]
    $b = $immutableIdentity.Platforms[$arch]
    if (-not $b -or $b.Digest -ne $a.Digest -or $b.ConfigDigest -ne $a.ConfigDigest -or
        $b.Revision -ne $commit) {
      Fail "sha-<commit> identity differs on $arch (index/child/config must all match)"
    }
  }

  # Exact-commit main CI: a checkbox is never evidence.
  $runsRaw = (Invoke-Gh -Arguments @('run', 'list', '--workflow', 'ci.yml',
      '--event', 'push', '--branch', 'main', '--commit', $commit,
      '--json', 'status,conclusion', '--limit', '1')) -join "`n"
  $ciVerified = $false
  try {
    $runs = $runsRaw | ConvertFrom-Json
    if (@($runs).Count -gt 0 -and $runs[0].status -eq 'completed' -and $runs[0].conclusion -eq 'success') {
      $ciVerified = $true
    }
  } catch { $ciVerified = $false }
  if (-not $ciVerified) { Fail "no successful completed push CI for exact commit $commit" }

  # A latest-main dev update requires exact identity with current main. GitHub
  # compare A...B reports ahead_by for commits in B not in A; requiring both
  # counts to be zero rejects a stale/nightly-lagging target explicitly.
  $compareRaw = (Invoke-Gh -Arguments @('api',
      "repos/$GitHubRepo/compare/$commit...$mainSha",
      '--jq', '{ahead:.ahead_by,behind:.behind_by,status:.status}')) -join "`n"
  $compare = $compareRaw | ConvertFrom-Json
  if ([int]$compare.ahead -ne 0 -or [int]$compare.behind -ne 0 -or
      [string]$compare.status -ne 'identical') {
    Fail "target is not current main (ahead=$($compare.ahead) behind=$($compare.behind) status=$($compare.status))"
  }

  $platform = Get-LocalPlatform
  $selected = $aliasIdentity.Platforms[$platform]
  Write-Step "registry identity index=$aliasDigest child=$($selected.Digest) config=$($selected.ConfigDigest) platform=$platform"

  return [pscustomobject]@{
    Project = $ProdProject
    Service = $ProdService
    Endpoint = "http://127.0.0.1:$ProdPort"
    Compose = $ProdCompose
    DataDir = $ProdDataDir
    BackupsDir = $ProdBackupsDir
    ExpectedMounts = @(
      "$ProdDataDir`:$('/app/data'):rw",
      "$ProdBackupsDir`:$('/app/backups'):rw",
      "$('/opt/carbon-panel-host/tmp')`:$('/app/tmp'):rw",
      '/var/run/docker.sock:/var/run/docker.sock:rw'
    )
    CurrentCommit = ''
    Commit = $commit
    Digest = $aliasDigest
    IndexDigest = $aliasDigest
    PlatformDigest = $selected.Digest
    ConfigDigest = $selected.ConfigDigest
    Platform = $platform
    PreviousCommit = ''
    PreviousDigest = ''
    CiVerified = $ciVerified
    CompareStatus = [string]$compare.status
    MainIdentical = $true
    LocalForwardOk = $true
    Architectures = $RequiredArchitectures
    RevisionMatches = $true
    LocalRef = "$RegistryImage@$aliasDigest"
    SkipPull = $false
    Fixture = $false
  }
}

function Get-ComposeImage {
  param([Parameter(Mandatory = $true)]$Target)
  $images = @(Invoke-Compose -Target $Target -Arguments @('config', '--images') |
    Where-Object { $_ -and $_.Trim() })
  if ($images.Count -lt 1) { Fail 'compose config --images returned no image' }
  return $images[0].Trim()
}

# --- Container guards -------------------------------------------------------
function Resolve-Container {
  param([Parameter(Mandatory = $true)]$Target)
  # Compose may name the container explicitly (the local install uses
  # container_name: carbon-panel), so resolve by the immutable compose labels
  # rather than by a naming convention. Exactly one non-oneoff container must
  # match; identity and mount guards are applied afterwards.
  $all = Invoke-Docker -Arguments @('ps', '-a',
    '--filter', "label=com.docker.compose.project=$($Target.Project)",
    '--filter', "label=com.docker.compose.service=$($Target.Service)",
    '--filter', 'label=com.docker.compose.oneoff=False',
    '--format', '{{.Names}}') -AllowFailure
  $found = @($all | Where-Object { $_ -and $_.Trim() })
  if ($found.Count -ne 1) {
    Fail "expected exactly one scoped container for project=$($Target.Project) service=$($Target.Service); got $($found.Count)"
  }
  return $found[0].Trim()
}

function Assert-ContainerIdentity {
  param([Parameter(Mandatory = $true)]$Target, [Parameter(Mandatory = $true)][string]$Container)

  $inspect = Invoke-DockerJson -Arguments @('inspect', $Container)
  $labels = $inspect.Config.Labels
  if (-not $labels) { Fail 'container has no labels' }
  if ([string]$labels.'com.docker.compose.project' -ne $Target.Project) { Fail 'compose project label mismatch' }
  if ([string]$labels.'com.docker.compose.service' -ne $Target.Service) { Fail 'compose service label mismatch' }
  if ([string]$labels.'com.docker.compose.oneoff' -eq 'True') { Fail 'refusing a one-off container' }
  if ($Target.Fixture) {
    if ([string]$labels.'carbonpanel.devnightly.fixture' -ne 'true') {
      Fail 'fixture scope requires the task-owned fixture marker label'
    }
  } else {
    if ([string]$labels.'carbonpanel.devnightly.fixture' -eq 'true') {
      Fail 'production scope refuses a fixture-marked container'
    }
  }

  $actual = @()
  foreach ($m in @($inspect.Mounts)) {
    $mode = if ($m.RW) { 'rw' } else { 'ro' }
    $actual += ("{0}:{1}:{2}" -f $m.Source, $m.Destination, $mode)
  }
  $actual = @($actual | Sort-Object)
  $expected = @($Target.ExpectedMounts | Sort-Object)
  if (($actual -join '|') -ne ($expected -join '|')) {
    Fail ("mount guard failed; expected [{0}] got [{1}]" -f ($expected -join ', '), ($actual -join ', '))
  }

  $portOk = $false
  $ports = $inspect.NetworkSettings.Ports
  if ($ports) {
    $key = '8080/tcp'
    $bindings = $ports.$key
    if ($bindings) {
      foreach ($b in @($bindings)) {
        if ([string]$b.HostIp -eq '127.0.0.1' -and [string]$b.HostPort -eq ([string]([uri]$Target.Endpoint).Port)) {
          $portOk = $true
        }
      }
    }
  }
  if (-not $portOk) { Fail 'loopback port binding does not match the scoped endpoint' }

  $imageId = [string]$inspect.Image
  if ($imageId -notmatch '^sha256:[0-9a-f]{64}$') { Fail 'container has no immutable image digest' }
  return $inspect
}

function Get-ImageRevision {
  param([Parameter(Mandatory = $true)][string]$Reference)
  $labelsRaw = (Invoke-Docker -Arguments @('image', 'inspect', $Reference,
      '--format', '{{json .Config.Labels}}')) -join "`n"
  try { $labels = $labelsRaw | ConvertFrom-Json } catch { return '' }
  if (-not $labels) { return '' }
  $rev = [string]$labels.'org.opencontainers.image.revision'
  if (-not $rev) { $rev = [string]$labels.revision }
  return $rev
}

function Assert-NotDownward {
  param([Parameter(Mandatory = $true)]$Target, [Parameter(Mandatory = $true)][string]$CurrentCommit)
  if ($CurrentCommit -notmatch '^[0-9a-f]{40}$') {
    Fail 'cannot prove a non-downward move: current image has no commit label'
  }
  if ($CurrentCommit -eq $Target.Commit) { return }
  if ($Target.Fixture) {
    $order = @($Target.CommitOrder)
    $currentIndex = [array]::IndexOf($order, $CurrentCommit)
    $targetIndex = [array]::IndexOf($order, $Target.Commit)
    if ($currentIndex -lt 0 -or $targetIndex -lt 0) {
      Fail 'fixture commit order cannot classify the local forward move'
    }
    if ($targetIndex -lt $currentIndex) {
      Fail "fixture target is below the running local commit (local forward-only check)"
    }
    return
  }
  # LOCAL FORWARD-ONLY CHECK. For GitHub compare/CURRENT...TARGET, ahead_by is
  # commits in TARGET (head) missing from CURRENT (base) — the forward move —
  # and behind_by is commits in CURRENT missing from TARGET — a downward move.
  # Refuse any behind_by > 0; identical or ahead is safe.
  $raw = (Invoke-Gh -Arguments @('api',
      "repos/$GitHubRepo/compare/$CurrentCommit...$($Target.Commit)",
      '--jq', '{ahead:.ahead_by,behind:.behind_by,status:.status}')) -join "`n"
  $cmp = $raw | ConvertFrom-Json
  if ([int]$cmp.behind -ne 0) {
    Fail "target is behind the running local commit (behind=$($cmp.behind)); refusing a downward update"
  }
  if (@('ahead', 'identical') -notcontains [string]$cmp.status) {
    Fail "target is not a forward move from the running commit (status=$($cmp.status))"
  }
}

# --- SQLite online backup ---------------------------------------------------
function New-DatabaseBackup {
  param([Parameter(Mandatory = $true)]$Target, [Parameter(Mandatory = $true)][string]$Stamp)
  $db = "$($Target.DataDir)/carbon-panel.db"
  $dest = "$($Target.BackupsDir)/devnightly-$Stamp.sqlite"
  $code = @'
import sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
source = sqlite3.connect("file:" + src + "?mode=ro", uri=True)
target = sqlite3.connect(dst)
try:
    source.backup(target)
finally:
    target.close()
    source.close()
check = sqlite3.connect("file:" + dst + "?mode=ro", uri=True)
try:
    assert check.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    tables = check.execute("select count(*) from sqlite_master where type='table'").fetchone()[0]
    servers = check.execute("select count(*) from servers").fetchone()[0] if tables else -1
    print("BACKUP_OK tables=%d servers=%d" % (tables, servers))
finally:
    check.close()
'@
  $result = Invoke-WslPython -Code $code -Args @($db, $dest)
  if (-not (($result -join ' ') -match 'BACKUP_OK')) { Fail 'SQLite online backup or integrity check failed' }
  return [pscustomobject]@{ Path = $dest; Result = ($result -join ' ').Trim() }
}

function Get-DatabaseMetrics {
  param([Parameter(Mandatory = $true)]$Target)
  $db = "$($Target.DataDir)/carbon-panel.db"
  $code = @'
import sqlite3, sys
c = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
try:
    assert c.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    tables = c.execute("select count(*) from sqlite_master where type='table'").fetchone()[0]
    servers = c.execute("select count(*) from servers").fetchone()[0]
    print("DATA_OK tables=%d servers=%d" % (tables, servers))
finally:
    c.close()
'@
  $result = Invoke-WslPython -Code $code -Args @($db)
  if (-not (($result -join ' ') -match 'DATA_OK')) { Fail 'post-update SQLite verification failed' }
  return ($result -join ' ').Trim()
}

function Get-DatabaseState {
  # State, not error text, decides whether a restore is safe. user_version
  # detects a committed schema migration; the fingerprint covers the database
  # plus WAL/SHM bytes so post-snapshot writes cannot be mistaken for safety.
  param([Parameter(Mandatory = $true)]$Target)
  $db = "$($Target.DataDir)/carbon-panel.db"
  $code = @'
import sqlite3, sys, hashlib, os
db = sys.argv[1]
c = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
try:
    uv = int(c.execute("PRAGMA user_version").fetchone()[0])
    tables = int(c.execute("select count(*) from sqlite_master where type='table'").fetchone()[0])
    servers = int(c.execute("select count(*) from servers").fetchone()[0]) if tables else -1
finally:
    c.close()
h = hashlib.sha256()
for suffix in ("", "-wal", "-shm"):
    path = db + suffix
    if os.path.exists(path):
        with open(path, "rb") as handle:
            h.update(handle.read())
print("DBSTATE user_version=%d tables=%d servers=%d fingerprint=%s" %
      (uv, tables, servers, h.hexdigest()))
'@
  $text = ((Invoke-WslPython -Code $code -Args @($db)) -join ' ')
  if ($text -notmatch 'DBSTATE user_version=(\d+) tables=(\d+) servers=(-?\d+) fingerprint=([0-9a-f]{64})') {
    Fail "cannot read database state for a safe rollback decision: $text"
  }
  return [pscustomobject]@{
    UserVersion = [int]$Matches[1]
    Tables = [int]$Matches[2]
    Servers = [int]$Matches[3]
    Fingerprint = $Matches[4]
    Summary = $text.Trim()
  }
}

function Test-FixtureLossMarker {
  # Qualification-only explicit declaration of an injected data loss. Never
  # consulted in Production scope; production ambiguity always fails closed.
  param([Parameter(Mandatory = $true)]$Target)
  if (-not $Target.Fixture) { return $false }
  $marker = "$($Target.DataDir)/.devnightly-fixture-loss-marker"
  $code = @'
import os, sys
p = sys.argv[1]
print("MARKER_OK" if os.path.exists(p) and open(p).read().strip() == "FIXTURE_LOSS_v1" else "MARKER_ABSENT")
'@
  $result = Invoke-WslPython -Code $code -Args @($marker)
  return (($result -join ' ') -match 'MARKER_OK')
}

function Get-RestoreDecision {
  param([Parameter(Mandatory = $true)]$Target,
        [Parameter(Mandatory = $true)]$Baseline,
        [Parameter(Mandatory = $true)]$Failure)

  if ($Failure.Fingerprint -eq $Baseline.Fingerprint -and
      $Failure.UserVersion -eq $Baseline.UserVersion) {
    return [pscustomobject]@{ Action = 'recreate'; Reason = 'database has no post-snapshot writes; recreate the old image only' }
  }
  if ($Failure.UserVersion -gt $Baseline.UserVersion) {
    return [pscustomobject]@{ Action = 'fail-closed'; Reason = "committed schema migration after snapshot (user_version $($Baseline.UserVersion) -> $($Failure.UserVersion)); automatic restore would overwrite newer state" }
  }
  if (Test-FixtureLossMarker -Target $Target) {
    return [pscustomobject]@{ Action = 'restore-and-recreate'; Reason = 'fixture-injected loss marker authorizes restore of the verified backup' }
  }
  return [pscustomobject]@{ Action = 'fail-closed'; Reason = 'post-snapshot database state is ambiguous; refusing to auto-restore over possible newer data' }
}

# --- Health -----------------------------------------------------------------
function Assert-Health {
  param([Parameter(Mandatory = $true)][string]$Endpoint, [Parameter(Mandatory = $true)][int]$TimeoutSeconds)
  $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
  $last = ''
  while ((Get-Date) -lt $deadline) {
    foreach ($path in @('/healthz', '/readyz', '/health/detail')) {
      try {
        $res = Invoke-WebRequest -Uri ($Endpoint + $path) -UseBasicParsing -TimeoutSec 5
        if ([int]$res.StatusCode -ne 200) { $last = "$path HTTP $($res.StatusCode)" }
      } catch {
        $last = "$path $($_.Exception.Message)"
      }
    }
    if (-not $last) { return }
    $last = ''
    Start-Sleep -Seconds 2
  }
  Fail "health/readiness verification timed out: $last"
}

# --- Lock and rollback ------------------------------------------------------
function Acquire-Lock {
  param([Parameter(Mandatory = $true)][string]$Project)
  $dir = Get-LockDir
  if (-not (Test-Path -LiteralPath $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
  $path = Join-Path $dir "$Project.devnightly.lock"
  $deadline = (Get-Date).AddSeconds(30)
  while ((Get-Date) -lt $deadline) {
    try {
      $stream = [System.IO.File]::Open($path, [System.IO.FileMode]::CreateNew,
        [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
      $writer = New-Object System.IO.StreamWriter($stream)
      $writer.WriteLine("pid=$PID")
      $writer.WriteLine("started=$((Get-Date).ToUniversalTime().ToString('o'))")
      $writer.Flush()
      return [pscustomobject]@{ Path = $path; Stream = $stream; Writer = $writer }
    } catch {
      Start-Sleep -Milliseconds 500
    }
  }
  Fail "durable lock is held: $path"
}

function Release-Lock {
  param($Lock)
  if (-not $Lock) { return }
  try { $Lock.Writer.Dispose() } catch { }
  try { $Lock.Stream.Dispose() } catch { }
  try { Remove-Item -LiteralPath $Lock.Path -Force } catch { }
}

function Get-RunningDigest {
  param([Parameter(Mandatory = $true)][string]$Container)
  $raw = Invoke-Docker -Arguments @('inspect', $Container, '--format', '{{.Image}}')
  return ([string]($raw -join '')).Trim()
}

function Register-Rollback {
  param([Parameter(Mandatory = $true)]$Target, [Parameter(Mandatory = $true)][string]$Stamp,
        [Parameter(Mandatory = $true)][string]$Digest)
  $tag = "$RollbackTagPrefix$Stamp"
  Invoke-Docker -Arguments @('tag', $Digest, $tag) | Out-Null
  $tagged = Get-ImageDigest -Reference $tag
  if ($tagged -ne $Digest) { Fail 'rollback tag does not resolve to the pre-update image' }
  return $tag
}

function Get-ImageDigest {
  param([Parameter(Mandatory = $true)][string]$Reference)
  $raw = Invoke-Docker -Arguments @('image', 'inspect', $Reference, '--format', '{{.Id}}') -AllowFailure
  return ([string]($raw -join '')).Trim()
}

function Limit-RollbackTags {
  # Bounded protection: only tags created by this tool are candidates, and the
  # running image is never untagged.
  param([Parameter(Mandatory = $true)][string]$KeepTag)
  $all = Invoke-Docker -Arguments @('image', 'ls', '--format', '{{.Repository}}:{{.Tag}}') -AllowFailure
  $tags = @($all | Where-Object { $_ -and $_.StartsWith($RollbackTagPrefix) -and $_ -ne $KeepTag } | Sort-Object)
  while ($tags.Count -ge $MaxRollbackTags) {
    $oldest = $tags[0]
    Invoke-Docker -Arguments @('image', 'rm', $oldest) -AllowFailure | Out-Null
    $tags = @($tags | Where-Object { $_ -ne $oldest })
  }
}

function Invoke-Compose {
  param([Parameter(Mandatory = $true)]$Target, [string[]]$Arguments = @(), [switch]$AllowFailure)
  return Invoke-Docker -Arguments (@('compose', '-p', $Target.Project, '-f', $Target.Compose) + $Arguments) -AllowFailure:$AllowFailure
}

function Invoke-Rollback {
  param([Parameter(Mandatory = $true)]$Target, [Parameter(Mandatory = $true)][string]$RollbackTag,
        $Backup, [Parameter(Mandatory = $true)][string]$Container,
        [Parameter(Mandatory = $true)][string]$RollbackDigest,
        [Parameter(Mandatory = $true)][string]$ComposeImage, $BaselineState,
        [switch]$RestoreDatabase)
  if ($RestoreDatabase) {
    Write-Step 'ROLLBACK starting: verified DB restore then scoped controller recreation'
  } else {
    Write-Step 'ROLLBACK starting: image-only recreation of the scoped controller (no DB restore)'
  }
  # If the database may be damaged, quiesce ONLY this service before restore.
  # No other container, game server or WSL service is stopped or restarted.
  if ($RestoreDatabase) {
    if (-not $Backup) { Fail 'ROLLBACK cannot restore the database: no verified backup' }
    Invoke-Compose -Target $Target -Arguments @('stop', $Target.Service) | Out-Null
    $db = "$($Target.DataDir)/carbon-panel.db"
    $code = @'
import sqlite3, sys
backup, db = sys.argv[1], sys.argv[2]
source = sqlite3.connect("file:" + backup + "?mode=ro", uri=True)
target = sqlite3.connect(db)
try:
    source.backup(target)
finally:
    target.close()
    source.close()
check = sqlite3.connect("file:" + db + "?mode=ro", uri=True)
try:
    assert check.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    print("RESTORE_OK")
finally:
    check.close()
'@
    $result = Invoke-WslPython -Code $code -Args @($Backup.Path, $db)
    if (-not (($result -join ' ') -match 'RESTORE_OK')) {
      Fail 'ROLLBACK database restore failed: verified backup could not be restored'
    }
    if ($Target.Fixture) {
      $marker = "$($Target.DataDir)/.devnightly-fixture-loss-marker"
      $clear = @'
import os, sys
p = sys.argv[1]
if os.path.exists(p):
    os.remove(p)
print("MARKER_CLEARED")
'@
      $null = Invoke-WslPython -Code $clear -Args @($marker)
    }
  }

  Invoke-Docker -Arguments @('tag', $RollbackTag, $ComposeImage) | Out-Null
  # Failure here is fatal: a recreate that cannot start is not a rollback.
  Invoke-Compose -Target $Target -Arguments @('up', '-d', '--no-deps', '--pull', 'never',
    '--force-recreate', $Target.Service) | Out-Null

  $problems = @()
  $digest = Get-RunningDigest -Container $Container
  if ($digest -ne $RollbackDigest) { $problems += "image config ID mismatch: $digest" }
  try { Assert-ContainerIdentity -Target $Target -Container $Container }
  catch { $problems += "mount/identity guard failed: $($_.Exception.Message)" }
  try { Assert-Health -Endpoint $Target.Endpoint -TimeoutSeconds $HealthTimeoutSeconds }
  catch { $problems += "health/readiness failed: $($_.Exception.Message)" }
  try {
    $restoredData = Get-DatabaseMetrics -Target $Target
    if ($BaselineState) {
      $restoredTables = [int]([regex]::Match($restoredData, 'tables=(\d+)').Groups[1].Value)
      $restoredServers = [int]([regex]::Match($restoredData, 'servers=(\d+)').Groups[1].Value)
      if ($restoredTables -ne $BaselineState.Tables -or $restoredServers -ne $BaselineState.Servers) {
        $problems += ("restored data differs from baseline: tables {0}->{1} servers {2}->{3}" -f `
          $BaselineState.Tables, $restoredTables, $BaselineState.Servers, $restoredServers)
      }
    }
    Write-Step "ROLLBACK data verified: $restoredData"
  } catch { $problems += "database recovery failed: $($_.Exception.Message)" }
  if ($problems.Count -gt 0) {
    Fail ("ROLLBACK DID NOT VERIFY: " + ($problems -join '; '))
  }
  Write-Step "ROLLBACK VERIFIED: running image config ID $digest, mounts/health/data ok"
}

# --- Main flow --------------------------------------------------------------
$script:RollbackDigest = ''
$lock = $null
try {
  if ($Scope -eq 'Fixture' -and -not $FixtureManifest) {
    Fail 'Fixture scope requires -FixtureManifest'
  }
  if ($Scope -eq 'Production' -and $FixtureManifest) {
    Fail 'Production scope does not accept target overrides or a fixture manifest'
  }
  if ($SimulateDataFailure -and $Scope -ne 'Fixture') {
    Fail '-SimulateDataFailure is only valid in Fixture scope'
  }
  if ($FixtureCorruptDatabase -and $Scope -ne 'Fixture') {
    Fail '-FixtureCorruptDatabase is only valid in Fixture scope'
  }
  if ($FixtureCommitMigration -and $Scope -ne 'Fixture') {
    Fail '-FixtureCommitMigration is only valid in Fixture scope'
  }
  if ($FixtureForceImageFailure -and $Scope -ne 'Fixture') {
    Fail '-FixtureForceImageFailure is only valid in Fixture scope'
  }

  $config = [pscustomobject]@{ Scope = $Scope; FixtureManifest = $FixtureManifest }
  $target = Resolve-TrustedTarget -Config $config
  if (-not $target.CiVerified) { Fail 'unqualified target: exact main CI is not verified' }
  if ($target.Architectures -notcontains 'linux/amd64' -or $target.Architectures -notcontains 'linux/arm64') {
    Fail 'target image lacks both required Linux architectures'
  }
  if (-not $target.RevisionMatches) { Fail 'target image revision does not match the pinned commit' }
  if (-not $target.MainIdentical) { Fail 'target is not exactly current main' }
  Write-Step "scope=$Scope project=$($target.Project) service=$($target.Service) endpoint=$($target.Endpoint)"
  Write-Step "target commit=$($target.Commit) ci=$($target.CiVerified) compare=$($target.CompareStatus)"
  Write-Step "identity index=$($target.IndexDigest) child=$($target.PlatformDigest) config=$($target.ConfigDigest) platform=$($target.Platform)"

  # The lock precedes EVERY state baseline read: no TOCTOU gap between the
  # observations used to authorize the write and the mutation itself.
  $lock = Acquire-Lock -Project $target.Project

  $container = Resolve-Container -Target $target
  $inspect = Assert-ContainerIdentity -Target $target -Container $container
  $currentConfigId = [string]$inspect.Image
  $currentCommit = Get-ImageRevision -Reference $currentConfigId
  Assert-NotDownward -Target $target -CurrentCommit $currentCommit
  Assert-Health -Endpoint $target.Endpoint -TimeoutSeconds $HealthTimeoutSeconds
  $beforeData = Get-DatabaseMetrics -Target $target
  Write-Step "baseline config-id=$currentConfigId commit=$currentCommit data=$beforeData"

  # Config ID (image inspect .Id / container inspect .Image) is compared ONLY
  # to the selected platform's image CONFIG digest, never to the index digest.
  if ($currentConfigId -eq $target.ConfigDigest) {
    Write-Step 'NO-OP: the scoped service already runs the verified target config ID'
    exit 0
  }

  $stamp = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
  $composeImage = Get-ComposeImage -Target $target
  Write-Step 'DRY-RUN complete; no pull, backup, tag or recreation was performed'
  if (-not $Apply) {
    if ($target.SkipPull) {
      Write-Step "would verify pre-built fixture image $($target.LocalRef) before any downtime"
    } else {
      Write-Step "would pull and verify $($target.LocalRef) before any downtime"
    }
    Write-Step "would resolve config ID $($target.ConfigDigest) from child $($target.PlatformDigest)"
    Write-Step "would tag it as $composeImage"
    Write-Step "would snapshot $($target.DataDir)/carbon-panel.db via the SQLite online backup API"
    Write-Step "would preserve $currentConfigId as a bounded rollback tag"
    Write-Step "would run: docker --host $DockerHost compose -p $($target.Project) -f $($target.Compose) up -d --no-deps --pull never --force-recreate $($target.Service)"
    Write-Step 'would verify health, mounts, image config ID and data; roll back on failure'
    exit 0
  }

  # Resolve/pull the pinned INDEX digest before any downtime or write; fixture
  # scope uses a pre-built task-owned image instead of the trusted registry.
  if ($target.SkipPull) {
    Write-Step "fixture: using pre-built image $($target.LocalRef) (no registry pull)"
    $localConfigId = Get-ImageDigest -Reference $target.LocalRef
  } else {
    Write-Step "pulling pinned index digest $($target.LocalRef) before downtime"
    Invoke-Docker -Arguments @('pull', $target.LocalRef) | Out-Null
    $localConfigId = Get-ImageDigest -Reference $target.LocalRef
  }
  if ($localConfigId -ne $target.ConfigDigest) {
    Fail "resolved local config ID $localConfigId differs from verified config $($target.ConfigDigest)"
  }

  $backup = New-DatabaseBackup -Target $target -Stamp "pre-$stamp"
  # Capture STATE immediately after the verified snapshot. Rollback safety is
  # decided from this state versus the state at failure time, never from the
  # wording of the error that triggered the failure.
  $baselineState = Get-DatabaseState -Target $target
  Write-Step "backup baseline state: $($baselineState.Summary)"
  $rollbackTag = Register-Rollback -Target $target -Stamp $stamp -Digest $currentConfigId
  $script:RollbackDigest = $currentConfigId
  Limit-RollbackTags -KeepTag $rollbackTag

  $failed = $false
  $failureReason = ''
  try {
    Invoke-Docker -Arguments @('tag', $localConfigId, $composeImage) | Out-Null
    $tagged = Get-ImageDigest -Reference $composeImage
    if ($tagged -ne $target.ConfigDigest) { throw 're-tagged image config ID does not match the target' }
    Invoke-Compose -Target $target -Arguments @('up', '-d', '--no-deps', '--pull', 'never',
      '--force-recreate', $target.Service) | Out-Null
    Assert-Health -Endpoint $target.Endpoint -TimeoutSeconds $HealthTimeoutSeconds
    $after = Assert-ContainerIdentity -Target $target -Container $container
    if ([string]$after.Image -ne $target.ConfigDigest) {
      throw 'recreated container is not running the verified platform config ID'
    }
    if ($FixtureForceImageFailure) {
      # Qualification-only failure AFTER the immutable image was pulled and the
      # service recreated, but BEFORE any database hook runs. This exercises a
      # production-style IMAGE-ONLY rollback with an unchanged database.
      Write-Step 'fixture: forced post-pull failure before any database mutation'
      throw 'fixture-forced post-pull failure (image rollback rehearsal)'
    }
    if ($FixtureCorruptDatabase) {
      # Fixture-only REAL data-loss injection: delete controller rows from the
      # live fixture database AFTER the verified backup was taken. The normal
      # data check must detect it and the rollback must restore the backup.
      Write-Step 'fixture: deleting live controller rows to exercise real DB restore'
      $liveDb = "$($Target.DataDir)/carbon-panel.db"
      $damage = @'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("DELETE FROM servers")
c.commit()
c.close()
print("FIXTURE_DB_DAMAGED")
'@
      $damaged = Invoke-WslPython -Code $damage -Args @($liveDb)
      if (-not (($damaged -join ' ') -match 'FIXTURE_DB_DAMAGED')) {
        throw 'fixture data-loss injection failed'
      }
      # Explicit qualification-only declaration: this change was INJECTED, not
      # legitimate newer state. Production never consults this marker.
      $marker = "$($target.DataDir)/.devnightly-fixture-loss-marker"
      $mark = @'
import sys
open(sys.argv[1], "w").write("FIXTURE_LOSS_v1")
print("MARKER_WRITTEN")
'@
      $marked = Invoke-WslPython -Code $mark -Args @($marker)
      if (-not (($marked -join ' ') -match 'MARKER_WRITTEN')) {
        throw 'fixture loss marker could not be written'
      }
    }
    if ($FixtureCommitMigration) {
      # Qualification-only committed migration AFTER the snapshot: this is
      # legitimate newer schema state and must never be auto-restored over.
      Write-Step 'fixture: committing a post-snapshot schema migration'
      $migrateDb = "$($target.DataDir)/carbon-panel.db"
      $migrate = @'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("PRAGMA user_version = %d" % (c.execute("PRAGMA user_version").fetchone()[0] + 1))
c.commit()
c.close()
print("FIXTURE_MIGRATION_COMMITTED")
'@
      $migrated = Invoke-WslPython -Code $migrate -Args @($migrateDb)
      if (-not (($migrated -join ' ') -match 'FIXTURE_MIGRATION_COMMITTED')) {
        throw 'fixture migration injection failed'
      }
    }
    if ($SimulateDataFailure) { throw 'data verification failed (simulated fixture failure)' }
    $afterData = Get-DatabaseMetrics -Target $target
    $beforeTables = [int]([regex]::Match($beforeData, 'tables=(\d+)').Groups[1].Value)
    $afterTables = [int]([regex]::Match($afterData, 'tables=(\d+)').Groups[1].Value)
    $beforeServers = [int]([regex]::Match($beforeData, 'servers=(\d+)').Groups[1].Value)
    $afterServers = [int]([regex]::Match($afterData, 'servers=(\d+)').Groups[1].Value)
    if ($afterTables -lt $beforeTables -or $afterServers -lt $beforeServers) {
      throw 'data verification failed: tables or servers decreased after update'
    }
    Write-Step "UPDATE OK config-id=$($after.Image) index=$($target.IndexDigest) child=$($target.PlatformDigest) data=$afterData rollback=$rollbackTag backup=$($backup.Path)"
  } catch {
    $failed = $true
    $failureReason = $_.Exception.Message
  }

  if ($failed) {
    Write-Step "UPDATE FAILED: $failureReason"
    # Decide from OBSERVED STATE, not error text. A generic readiness failure
    # can happen after a schema migration committed; restoring the pre-update
    # snapshot over that newer state would destroy legitimate data.
    try {
      $failureState = Get-DatabaseState -Target $target
      Write-Step "failure state: $($failureState.Summary)"
    } catch {
      # Unreadable state is itself ambiguous: withhold all automatic mutation.
      Fail "update failed; database state is unreadable, so automatic rollback is withheld. Backup: $($backup.Path). Error: $($_.Exception.Message)"
    }
    $decision = Get-RestoreDecision -Target $target -Baseline $baselineState -Failure $failureState
    if ($decision.Action -eq 'fail-closed') {
      Write-Step "FAIL-CLOSED: $($decision.Reason)"
      Write-Step "no database restore and no image recreation were performed; backup retained at $($backup.Path)"
      Write-Step 'operator action required: inspect the database state and choose an explicit recovery'
      Fail "update failed; automatic rollback withheld because state is ambiguous. $($decision.Reason)"
    }
    Write-Step "rollback decision: $($decision.Action) - $($decision.Reason)"
    Invoke-Rollback -Target $target -RollbackTag $rollbackTag -Backup $backup `
      -Container $container -RollbackDigest $script:RollbackDigest -ComposeImage $composeImage `
      -BaselineState $baselineState -RestoreDatabase:($decision.Action -eq 'restore-and-recreate')
    Fail "update failed; rollback verified ($($decision.Reason))"
  }
} finally {
  Release-Lock -Lock $lock
}
