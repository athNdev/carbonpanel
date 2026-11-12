# SPDX-License-Identifier: MPL-2.0
# Pure record fixtures exercise failure classification without public sockets or policy changes.
$ErrorActionPreference = 'Stop'
. "$PSScriptRoot/windows-verification-evidence.ps1"

function New-EvidenceFixture {
    [pscustomobject]@{
        Environment = [pscustomobject]@{
            IsWindows = $true
            UserSid = 'S-1-5-21-1-2-3-1002'
            IsAdministrator = $false
            EnableLUA = 1
            FirewallProfiles = @('Domain', 'Private', 'Public') | ForEach-Object {
                [pscustomobject]@{ Name = $_; Enabled = $true; NotifyOnListen = $true; DefaultInboundAction = 'Block' }
            }
            FirewallLog = [pscustomobject]@{ Enabled = $true; Readable = $true }
            Errors = @()
        }
        Observation = [pscustomobject]@{
            Commands = @([pscustomobject]@{ Command = 'go test -count=1 ./...'; Completed = $true; ExitCode = 0 })
            FreshExecutableCount = 5
            ObserverErrors = @()
            BeforeRecordId = 10L
            AfterRecordId = 10L
            OldestRecordId = 1L
            QueryUserEventCount = 0
            ExpectedUserSid = 'S-1-5-21-1-2-3-1002'
            ProfileWasAbsent = $true
            PositiveControlDetected = $true
        }
    }
}

function Assert-Evidence {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "Windows evidence check: $Message" }
}

$fixture = New-EvidenceFixture
$accepted = Test-WindowsVerificationEvidence $fixture.Environment $fixture.Observation
Assert-Evidence ($accepted.Accepted -and $accepted.Disposition -eq 'accepted-current-profile') 'valid current profile was rejected'
$accepted = Test-WindowsVerificationEvidence $fixture.Environment $fixture.Observation -RequireCleanProfile
Assert-Evidence ($accepted.Accepted -and $accepted.Disposition -eq 'accepted-clean-profile') 'valid disposable profile was rejected'
$serialized = $fixture | ConvertTo-Json -Depth 8
foreach ($decoded in @((ConvertFrom-Json $serialized), (ConvertFrom-Json $serialized -AsHashtable))) {
    $accepted = Test-WindowsVerificationEvidence $decoded.Environment $decoded.Observation -RequireCleanProfile
    Assert-Evidence $accepted.Accepted 'JSON evidence roundtrip changed the qualification result'
}

$cases = @(
    @{ Name = 'missing environment'; Change = { param($f) $f.Environment = $null } }
    @{ Name = 'non-Windows'; Change = { param($f) $f.Environment.IsWindows = $false } }
    @{ Name = 'elevated token'; Change = { param($f) $f.Environment.IsAdministrator = $true } }
    @{ Name = 'string token evidence'; Change = { param($f) $f.Environment.IsAdministrator = 'false' } }
    @{ Name = 'missing token SID'; Change = { param($f) $f.Environment.UserSid = $null } }
    @{ Name = 'malformed token SID'; Change = { param($f) $f.Environment.UserSid = 'user' } }
    @{ Name = 'unreadable UAC configuration'; Change = { param($f) $f.Environment.EnableLUA = $null } }
    @{ Name = 'string UAC policy'; Change = { param($f) $f.Environment.EnableLUA = '1' } }
    @{ Name = 'missing profile'; Change = { param($f) $f.Environment.FirewallProfiles = @($f.Environment.FirewallProfiles | Select-Object -First 2) } }
    @{ Name = 'disabled firewall'; Change = { param($f) $f.Environment.FirewallProfiles[0].Enabled = $false } }
    @{ Name = 'numeric false enum'; Change = { param($f) $f.Environment.FirewallProfiles[0].Enabled = 2 } }
    @{ Name = 'disabled notifications'; Change = { param($f) $f.Environment.FirewallProfiles[0].NotifyOnListen = $false } }
    @{ Name = 'permissive inbound default'; Change = { param($f) $f.Environment.FirewallProfiles[0].DefaultInboundAction = 'Allow' } }
    @{ Name = 'unreadable event channel'; Change = { param($f) $f.Environment.FirewallLog.Readable = $false } }
    @{ Name = 'disabled event channel'; Change = { param($f) $f.Environment.FirewallLog.Enabled = $false } }
    @{ Name = 'environment observer failed'; Change = { param($f) $f.Environment.Errors = @('access denied') } }
    @{ Name = 'missing environment completion'; Change = { param($f) $f.Environment.Errors = $null } }
    @{ Name = 'scalar environment completion'; Change = { param($f) $f.Environment.Errors = '' } }
    @{ Name = 'missing commands'; Change = { param($f) $f.Observation.Commands = @() } }
    @{ Name = 'scalar command'; Change = { param($f) $f.Observation.Commands = $f.Observation.Commands[0] } }
    @{ Name = 'unfinished command'; Change = { param($f) $f.Observation.Commands[0].Completed = $false } }
    @{ Name = 'missing command identity'; Change = { param($f) $f.Observation.Commands[0].Command = '' } }
    @{ Name = 'missing command exit'; Change = { param($f) $f.Observation.Commands[0].ExitCode = $null } }
    @{ Name = 'no rebuilt binary'; Change = { param($f) $f.Observation.FreshExecutableCount = 0 } }
    @{ Name = 'socket observer failed'; Change = { param($f) $f.Observation.ObserverErrors = @('CIM read failed') } }
    @{ Name = 'missing observer status'; Change = { param($f) $f.Observation.PSObject.Properties.Remove('ObserverErrors') } }
    @{ Name = 'null observer completion'; Change = { param($f) $f.Observation.ObserverErrors = $null } }
    @{ Name = 'scalar observer completion'; Change = { param($f) $f.Observation.ObserverErrors = '' } }
    @{ Name = 'missing record id'; Change = { param($f) $f.Observation.BeforeRecordId = $null } }
    @{ Name = 'reset log'; Change = { param($f) $f.Observation.AfterRecordId = 9L } }
    @{ Name = 'rolled-over log'; Change = { param($f) $f.Observation.OldestRecordId = 11L; $f.Observation.AfterRecordId = 12L } }
    @{ Name = 'impossible event bounds'; Change = { param($f) $f.Observation.OldestRecordId = 12L } }
    @{ Name = 'missing retained records'; Change = { param($f) $f.Observation.OldestRecordId = 0L } }
    @{ Name = 'empty log has retained record'; Change = { param($f) $f.Observation.BeforeRecordId = 0L; $f.Observation.AfterRecordId = 0L; $f.Observation.OldestRecordId = 1L } }
    @{ Name = 'initially empty log rolled over'; Change = { param($f) $f.Observation.BeforeRecordId = 0L; $f.Observation.OldestRecordId = 2L } }
    @{ Name = 'negative query count'; Change = { param($f) $f.Observation.QueryUserEventCount = -1 } }
)
foreach ($case in $cases) {
    $fixture = New-EvidenceFixture
    & $case.Change $fixture
    $result = Test-WindowsVerificationEvidence $fixture.Environment $fixture.Observation
    Assert-Evidence (-not $result.Accepted -and $result.Disposition -eq 'incomplete') "$($case.Name) was accepted as qualification"
}
foreach ($name in @('failed command', 'negative process status', 'query event')) {
    $fixture = New-EvidenceFixture
    if ($name -eq 'failed command') { $fixture.Observation.Commands[0].ExitCode = 1 }
    elseif ($name -eq 'negative process status') { $fixture.Observation.Commands[0].ExitCode = -1 }
    else { $fixture.Observation.QueryUserEventCount = 1 }
    $result = Test-WindowsVerificationEvidence $fixture.Environment $fixture.Observation
    Assert-Evidence (-not $result.Accepted -and $result.Disposition -eq 'failed') "$name was not a failed regression"
}
foreach ($name in @('ExpectedUserSid', 'ProfileWasAbsent', 'PositiveControlDetected')) {
    $fixture = New-EvidenceFixture
    if ($name -eq 'ExpectedUserSid') { $fixture.Observation.ExpectedUserSid = 'different-user' }
    else { $fixture.Observation.$name = $false }
    $result = Test-WindowsVerificationEvidence $fixture.Environment $fixture.Observation -RequireCleanProfile
    Assert-Evidence (-not $result.Accepted -and $result.Disposition -eq 'incomplete') "$name was not required for clean-profile proof"
}
$fixture = New-EvidenceFixture
$fixture.Environment.EnableLUA = 0
$result = Test-WindowsVerificationEvidence $fixture.Environment $fixture.Observation -RequireCleanProfile
Assert-Evidence ($result.Accepted -and $result.Warnings.Count -gt 0 -and $result.Scope -eq 'native-firewall-listener-regression') 'disabled UAC was silently treated as secure-desktop proof'
$fixture = New-EvidenceFixture
$fixture.Observation.BeforeRecordId = 0L
$fixture.Observation.AfterRecordId = 0L
$fixture.Observation.OldestRecordId = 0L
Assert-Evidence (Test-WindowsVerificationEvidence $fixture.Environment $fixture.Observation).Accepted 'a genuinely empty readable event log was rejected'
Write-Output 'PASS: Windows verification evidence accepts complete records and rejects failed/incomplete qualification.'
