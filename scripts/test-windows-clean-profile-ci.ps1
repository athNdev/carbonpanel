# SPDX-License-Identifier: MPL-2.0
$ErrorActionPreference = 'Stop'
$bootstrap = Join-Path $PSScriptRoot 'windows-clean-profile-ci.ps1'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($bootstrap, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw ($parseErrors | Out-String) }

# Load only the pure helpers, without running setup, to verify source/output
# boundaries and the one-command JSON case used after an early Go failure.
foreach ($definition in @($ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -in @('Test-PathInside', 'Write-QualificationJson', 'Convert-ProgramPath', 'Test-ProgramPathMatch', 'ConvertTo-FirewallQueryUserRecord', 'Get-AttributedQueries') }, $true))) {
    . ([scriptblock]::Create($definition.Extent.Text))
}
$testRoot = [IO.Path]::GetTempPath()
if (-not (Test-PathInside (Join-Path $testRoot 'child\evidence.json') $testRoot)) { throw 'Owned child path rejected.' }
if (Test-PathInside ($testRoot.TrimEnd('\', '/') + '-outside\evidence.json') $testRoot) { throw 'Sibling path accepted.' }
if (-not (Test-ProgramPathMatch '\??\C:\qualification\worker.exe' @('C:\qualification') -Descendants)) { throw 'NT path prefix rejected.' }
if (-not (Test-ProgramPathMatch '\Device\HarddiskVolume3\qualification\worker.exe' @('\Device\HarddiskVolume3\qualification') -Descendants)) { throw 'Device alias rejected.' }
if (Test-ProgramPathMatch 'C:\qualification-other\worker.exe' @('C:\qualification') -Descendants) { throw 'Event attribution accepted a sibling directory.' }
if (Test-ProgramPathMatch 'C:\unrelated\worker.exe' @('C:\qualification\worker.exe')) { throw 'Event attribution accepted a matching basename on another path.' }
function New-RuleEventFixture {
    param([string]$RuleId, [string]$ApplicationPath = '', [string]$Action = '2', [string]$ModifyingApplication = '')
    $fixture = [pscustomobject]@{ RecordId = 701; Xml = ('<Event xmlns="http://schemas.microsoft.com/win/2004/08/events/event"><EventData><Data Name="RuleId">{0}</Data><Data Name="ApplicationPath">{1}</Data><Data Name="Action">{2}</Data><Data Name="ModifyingApplication">{3}</Data></EventData></Event>' -f $RuleId, $ApplicationPath, $Action, $ModifyingApplication) }
    $fixture | Add-Member -MemberType ScriptMethod -Name ToXml -Value { $this.Xml }
    $fixture
}
$nativeId = 'TCP Query User{12345678-1234-1234-abcd-1234567890ab}c:\qualification\control.exe'
$ordinary = New-RuleEventFixture '{12345678-1234-1234-abcd-1234567890ab}' 'C:\qualification\control.exe' '2'
foreach ($action in @('2', '4')) {
    if ($null -ne (ConvertTo-FirewallQueryUserRecord (New-RuleEventFixture '{12345678-1234-1234-abcd-1234567890ab}' 'C:\qualification\control.exe' $action))) { throw 'An ordinary Event2097 rule with the owned app/action was misclassified as consent.' }
}
if ($null -ne (ConvertTo-FirewallQueryUserRecord (New-RuleEventFixture 'Raycast' '' '3'))) { throw 'Ordinary rule metadata without ApplicationPath was rejected or counted.' }
foreach ($protocol in @('TCP', 'UDP')) {
    $expectedNativeId = $nativeId.Replace('TCP', $protocol)
    $native = New-RuleEventFixture $expectedNativeId 'C:\qualification\control.exe' '2'
    $record = ConvertTo-FirewallQueryUserRecord $native
    if ($record.RuleId -ne $expectedNativeId -or $record.Action -ne '2' -or $record.ApplicationPath -ne 'C:\qualification\control.exe') { throw 'Native Query User record did not preserve reviewable metadata.' }
}
foreach ($invalid in @(
    (New-RuleEventFixture $nativeId '' '2' 'C:\qualification\control.exe'),
    (New-RuleEventFixture 'TCP Query User{bad-guid}' 'C:\qualification\control.exe'),
    (New-RuleEventFixture '' 'C:\qualification\control.exe'),
    (New-RuleEventFixture $nativeId 'C:\qualification\control.exe' '')
)) {
    $rejected = $false
    try { $null = ConvertTo-FirewallQueryUserRecord $invalid } catch { $rejected = $true }
    if (-not $rejected) { throw 'Incomplete or malformed Query User metadata was accepted.' }
}
# Exercise the actual event-provider/attribution seam with synthetic native
# records; no firewall provider query or public socket is used by these fixtures.
$script:ruleEventFixtures = @(
    $ordinary,
    (New-RuleEventFixture $nativeId 'C:\unrelated\control.exe'),
    (New-RuleEventFixture $nativeId 'C:\qualification-other\control.exe'),
    (New-RuleEventFixture $nativeId 'C:\qualification\control.exe')
)
function Get-WinEvent { $script:ruleEventFixtures }
$channel = 'synthetic-fixture'
$attributed = @(Get-AttributedQueries -After 0 -Aliases @('C:\qualification') -Descendants)
if ($attributed.Count -ne 1 -or $attributed[0].RuleId -ne $nativeId) { throw 'Native consent attribution did not enforce both RuleId and the owned application boundary.' }
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
