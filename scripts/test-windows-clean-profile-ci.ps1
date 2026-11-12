# SPDX-License-Identifier: MPL-2.0
$ErrorActionPreference = 'Stop'
$bootstrap = Join-Path $PSScriptRoot 'windows-clean-profile-ci.ps1'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($bootstrap, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw ($parseErrors | Out-String) }

# Load only the pure helpers, without running setup, to verify source/output
# boundaries and the one-command JSON case used after an early Go failure.
foreach ($definition in @($ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in @('Test-PathInside', 'Write-QualificationJson', 'Convert-ProgramPath', 'Test-ProgramPathMatch', 'Get-FirewallApplicationPath') }, $true))) {
    . ([scriptblock]::Create($definition.Extent.Text))
}
$testRoot = [IO.Path]::GetTempPath()
if (-not (Test-PathInside (Join-Path $testRoot 'child\evidence.json') $testRoot)) { throw 'Owned child path rejected.' }
if (Test-PathInside ($testRoot.TrimEnd('\', '/') + '-outside\evidence.json') $testRoot) { throw 'Sibling path accepted.' }
if (-not (Test-ProgramPathMatch '\??\C:\qualification\worker.exe' @('C:\qualification') -Descendants)) { throw 'NT path prefix rejected.' }
if (-not (Test-ProgramPathMatch '\Device\HarddiskVolume3\qualification\worker.exe' @('\Device\HarddiskVolume3\qualification') -Descendants)) { throw 'Device alias rejected.' }
if (Test-ProgramPathMatch 'C:\qualification-other\worker.exe' @('C:\qualification') -Descendants) { throw 'Event attribution accepted a sibling directory.' }
if (Test-ProgramPathMatch 'C:\unrelated\worker.exe' @('C:\qualification\worker.exe')) { throw 'Event attribution accepted a matching basename on another path.' }
$event = [pscustomobject]@{ Xml = '<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event"><EventData><Data Name="ModifyingApplication">C:\qualification\control.exe</Data><Data Name="ApplicationPath" /></EventData></Event>' }
$event | Add-Member -MemberType ScriptMethod -Name ToXml -Value { $this.Xml }
if (Get-FirewallApplicationPath $event) { throw 'ModifyingApplication was mistaken for the listening application.' }
$event.Xml = '<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event"><EventData><Data Name="ApplicationPath">C:\qualification\control.exe</Data></EventData></Event>'
if ((Get-FirewallApplicationPath $event) -ne 'C:\qualification\control.exe') { throw 'ApplicationPath was not read from the event namespace.' }
$jsonPath = [IO.Path]::GetTempFileName()
try {
    Write-QualificationJson @([pscustomobject]@{ Command = 'fixture'; Completed = $true; ExitCode = 1 }) $jsonPath
    if (-not (Get-Content -LiteralPath $jsonPath -Raw).TrimStart().StartsWith('[')) { throw 'Single command JSON lost its array shape.' }
} finally { Remove-Item -LiteralPath $jsonPath }

# Exercise the actual entry guard, while privileged operations are replaced by
# sentinels. No test sets the disposable-runner markers to their allowed values.
function New-LocalUser { throw 'MUTATION SENTINEL: account creation reached' }
function Set-NetFirewallProfile { throw 'MUTATION SENTINEL: firewall setup reached' }
$previous = $env:GITHUB_ACTIONS
try {
    $env:GITHUB_ACTIONS = 'false'
    try {
        & $bootstrap -AllowDisposableRunnerSetup
        throw 'Outside-runner setup unexpectedly succeeded'
    } catch {
        if ($_.Exception.Message -notmatch '^Clean-profile setup requires an explicitly opted-in disposable') { throw }
    }
} finally {
    $env:GITHUB_ACTIONS = $previous
}
Write-Output 'PASS: clean-profile setup parses and refuses execution outside a disposable hosted runner before mutation.'
