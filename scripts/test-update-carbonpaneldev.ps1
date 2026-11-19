# SPDX-License-Identifier: MPL-2.0
# Static contract checks for Update-CarbonPanelDev.ps1. No Docker, no WSL,
# no registry access and no secrets: safe to run in ordinary CI.
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script = Join-Path $PSScriptRoot 'Update-CarbonPanelDev.ps1'
$source = Get-Content -LiteralPath $script -Raw

$tokens = $null
$errors = $null
[System.Management.Automation.Language.Parser]::ParseFile($script, [ref]$tokens, [ref]$errors) | Out-Null
if ($errors.Count -ne 0) { throw "updater does not parse: $($errors[0])" }

$checks = @(
  'The default is dry-run', '[switch]$Apply', $true,
  'The Docker relay is fixed', "`$DockerHost = 'tcp://[::1]:2375'", $true,
  'The scoped project is fixed', "`$ProdProject = 'carbon-panel-host'", $true,
  'The scoped service is fixed', "`$ProdService = 'carbon-panel'", $true,
  'The scoped port is fixed', 'http://127.0.0.1:$ProdPort', $true,
  'tmp is a verified mount', '/app/tmp', $true,
  'docker.sock is a verified mount', '/var/run/docker.sock:/var/run/docker.sock:rw', $true,
  'Index digest is kept distinct', 'IndexDigest', $true,
  'Child manifest digest is kept distinct', 'PlatformDigest', $true,
  'Config digest is kept distinct', 'ConfigDigest', $true,
  'Running config ID is compared to the config digest', '$currentConfigId -eq $target.ConfigDigest', $true,
  'Pulled config ID is compared to the config digest', '$localConfigId -ne $target.ConfigDigest', $true,
  'No comparison of a config ID to an index digest', '$localDigest -ne $target.Digest', $false,
  'Exact-main compare requires zero ahead', '[int]$compare.ahead -ne 0', $true,
  'Exact-main compare requires zero behind', '[int]$compare.behind -ne 0', $true,
  'Forward-only local check', 'target is behind the running local commit', $true,
  'SQLite uses the online backup API', 'source.backup(target)', $true,
  'Lock precedes baseline', '$lock = Acquire-Lock -Project $target.Project', $true,
  'Fixture scope marker guard', 'fixture marker label', $true,
  'Rollback stops only the scoped service', "Invoke-Compose -Target `$Target -Arguments @('stop', `$Target.Service)", $true,
  'Rollback recreate cannot be tolerated', "'--force-recreate', `$Target.Service) | Out-Null", $true,
  'Rollback mismatch is fatal', 'ROLLBACK DID NOT VERIFY', $true,
  'Restore decision reads state, not error text', '$failureState = Get-DatabaseState -Target $target', $true,
  'Committed migration is detected', '$Failure.UserVersion -gt $Baseline.UserVersion', $true,
  'Ambiguity fails closed', "Action = 'fail-closed'", $true,
  'No error-text inference for DB safety', '$failureReason -match ''data|SQLite|integrity|database''', $false,
  'Operator message on ambiguity', 'operator action required', $true,
  'Forward-only check uses behind_by direction', 'if ([int]$cmp.behind -ne 0)', $true,
  'Forward-only check is not inverted', 'if ([int]$cmp.ahead -ne 0)', $false,
  'Rollback data is compared to baseline', '$restoredTables -ne $BaselineState.Tables', $true,
  'Image-only failure rehearsal switch', '[switch]$FixtureForceImageFailure', $true,
  'Image-only rollback decision exists', "Action = 'recreate'", $true
)
for ($i = 0; $i -lt $checks.Count; $i += 3) {
  $label = $checks[$i]
  $needle = $checks[$i + 1]
  $expected = $checks[$i + 2]
  $found = $source.Contains($needle)
  if ($found -ne $expected) {
    throw "$label : expected present=$expected, found=$found (needle: $needle)"
  }
  Write-Output "PASS: $label"
}

# Ordering: durable lock must be acquired before the baseline read.
$lockAt = $source.IndexOf('$lock = Acquire-Lock -Project $target.Project')
$baselineAt = $source.IndexOf('$beforeData = Get-DatabaseMetrics -Target $target')
if ($lockAt -lt 0 -or $baselineAt -lt 0 -or $lockAt -ge $baselineAt) {
  throw 'durable lock is not acquired before the state baseline'
}
Write-Output 'PASS: lock precedes baseline'

# Destructive operations that would touch other data are absent.
foreach ($banned in @('compose down -v', '--volumes', 'Remove-Item -Recurse', 'docker system prune', 'wsl --shutdown')) {
  if ($source.Contains($banned)) { throw "banned destructive token present: $banned" }
}
Write-Output 'PASS: no broad destructive operations'

Write-Output 'ALL UPDATE-CARBONPANELDEV CONTRACT CHECKS PASSED'
