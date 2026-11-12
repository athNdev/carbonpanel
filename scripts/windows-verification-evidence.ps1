# SPDX-License-Identifier: MPL-2.0
# Dot-source this file. Collection is read-only; evaluation never changes policy.

function Get-WindowsVerificationEnvironment {
    $issues = [Collections.Generic.List[string]]::new()
    $identity = $null
    $administrator = $null
    $lua = $null
    $profiles = @()
    $log = [ordered]@{ Enabled = $false; Readable = $false; LatestRecordId = $null; OldestRecordId = $null }
    $native = $env:OS -eq 'Windows_NT'
    if ($native) {
        try {
            $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
            $administrator = ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
        } catch { $issues.Add('Cannot read the current Windows token.') }
        try { $lua = Get-ItemPropertyValue -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System' -Name EnableLUA -ErrorAction Stop }
        catch { $issues.Add('Cannot read EnableLUA.') }
        try {
            $profiles = @(Get-NetFirewallProfile -PolicyStore ActiveStore -ErrorAction Stop | ForEach-Object {
                # These properties are CIM enums: False has the nonzero value 2.
                # A direct [bool] cast would incorrectly turn a disabled profile on.
                [pscustomobject]@{
                    Name = [string]$_.Name
                    Enabled = switch ([string]$_.Enabled) { 'True' { $true }; 'False' { $false }; default { $null } }
                    NotifyOnListen = switch ([string]$_.NotifyOnListen) { 'True' { $true }; 'False' { $false }; default { $null } }
                    DefaultInboundAction = [string]$_.DefaultInboundAction
                }
            })
        } catch { $issues.Add('Cannot read effective Windows Firewall profiles.') }
        try {
            $channel = 'Microsoft-Windows-Windows Firewall With Advanced Security/Firewall'
            $log.Enabled = [bool](Get-WinEvent -ListLog $channel -ErrorAction Stop).IsEnabled
            $log.LatestRecordId = 0L
            $log.OldestRecordId = 0L
            try {
                $log.LatestRecordId = [long](Get-WinEvent -LogName $channel -MaxEvents 1 -ErrorAction Stop).RecordId
                $log.OldestRecordId = [long](Get-WinEvent -LogName $channel -MaxEvents 1 -Oldest -ErrorAction Stop).RecordId
            } catch {
                if ($_.FullyQualifiedErrorId -notmatch '^NoMatchingEventsFound') { throw }
            }
            $log.Readable = $true
        } catch { $issues.Add('Cannot read the Windows Firewall event channel.') }
    } else { $issues.Add('Qualification requires native Windows.') }
    [pscustomobject]@{
        CapturedAtUtc = [datetime]::UtcNow.ToString('o')
        IsWindows = $native
        UserSid = if ($identity) { $identity.User.Value } else { $null }
        IsAdministrator = $administrator
        EnableLUA = $lua
        FirewallProfiles = $profiles
        FirewallLog = [pscustomobject]$log
        Errors = @($issues)
    }
}

function Get-WindowsEvidenceValue {
    param([object]$InputObject, [string]$Name)
    if ($null -eq $InputObject) { return $null }
    if ($InputObject -is [Collections.IDictionary]) {
        if ($InputObject.Contains($Name)) { return $InputObject[$Name] }
    } elseif ($InputObject.PSObject.Properties[$Name]) { return $InputObject.PSObject.Properties[$Name].Value }
    return $null
}

function Test-WindowsEvidenceInteger {
    param([object]$Value)
    (Test-WindowsEvidenceExitCode $Value) -and $Value -ge 0
}

function Test-WindowsEvidenceExitCode {
    param([object]$Value)
    $Value -is [int] -or $Value -is [long] -or $Value -is [uint32] -or $Value -is [uint64]
}

function Test-WindowsEvidenceArray {
    param([object]$InputObject, [string]$Name)
    $value = $null
    if ($InputObject -is [Collections.IDictionary] -and $InputObject.Contains($Name)) {
        $value = $InputObject[$Name]
    } elseif ($null -ne $InputObject -and $InputObject.PSObject.Properties[$Name]) {
        $value = $InputObject.PSObject.Properties[$Name].Value
    }
    $value -is [Collections.IList]
}

function Test-WindowsVerificationEvidence {
    param([object]$Environment, [object]$Observation, [switch]$RequireCleanProfile)
    $reasons = [Collections.Generic.List[string]]::new()
    $warnings = [Collections.Generic.List[string]]::new()
    $failed = $false
    if (-not (Test-WindowsEvidenceArray $Environment Errors)) { $reasons.Add('Environment collection errors were not recorded as a list.') }
    if ((Get-WindowsEvidenceValue $Environment IsWindows) -isnot [bool] -or -not $Environment.IsWindows) { $reasons.Add('Native Windows was not established.') }
    $userSid = Get-WindowsEvidenceValue $Environment UserSid
    if ($userSid -isnot [string] -or $userSid -notmatch '^S-\d+(?:-\d+)+$') { $reasons.Add('The Windows user SID was not recorded.') }
    $admin = Get-WindowsEvidenceValue $Environment IsAdministrator
    if ($admin -isnot [bool] -or $admin) { $reasons.Add('An unelevated token was not established.') }
    foreach ($errorEntry in @(Get-WindowsEvidenceValue $Environment Errors)) {
        if ($errorEntry) { $reasons.Add([string]$errorEntry) }
    }
    $lua = Get-WindowsEvidenceValue $Environment EnableLUA
    if (-not (Test-WindowsEvidenceInteger $lua) -or $lua -notin @(0, 1)) { $reasons.Add('EnableLUA was not recorded as an integer policy value.') }
    elseif ($lua -eq 0) { $warnings.Add('UAC is disabled in this environment; this result cannot establish secure-desktop UAC behavior.') }
    $profiles = @(Get-WindowsEvidenceValue $Environment FirewallProfiles)
    foreach ($name in @('Domain', 'Private', 'Public')) {
        $matching = @($profiles | Where-Object { (Get-WindowsEvidenceValue $_ Name) -eq $name })
        if ($matching.Count -ne 1) { $reasons.Add("Effective firewall profile $name was not uniquely recorded."); continue }
        $profile = $matching[0]
        $enabled = Get-WindowsEvidenceValue $profile Enabled
        $notify = Get-WindowsEvidenceValue $profile NotifyOnListen
        if ($enabled -isnot [bool] -or -not $enabled -or $notify -isnot [bool] -or -not $notify -or (Get-WindowsEvidenceValue $profile DefaultInboundAction) -ne 'Block') {
            $reasons.Add("Effective firewall profile $name must enable firewall and listen notifications with blocked default ingress.")
        }
    }
    $log = Get-WindowsEvidenceValue $Environment FirewallLog
    foreach ($name in @('Enabled', 'Readable')) {
        $value = Get-WindowsEvidenceValue $log $name
        if ($value -isnot [bool] -or -not $value) { $reasons.Add("Firewall event channel $name was not established.") }
    }
    $commands = @(Get-WindowsEvidenceValue $Observation Commands)
    if (-not (Test-WindowsEvidenceArray $Observation Commands) -or $commands.Count -eq 0 -or $null -eq $commands[0]) { $reasons.Add('No completed command list was recorded.') }
    foreach ($command in $commands) {
        if ([string]::IsNullOrWhiteSpace([string](Get-WindowsEvidenceValue $command Command))) { $reasons.Add('A command identity was not recorded.') }
        $complete = Get-WindowsEvidenceValue $command Completed
        $exitCode = Get-WindowsEvidenceValue $command ExitCode
        if ($complete -isnot [bool] -or -not $complete -or -not (Test-WindowsEvidenceExitCode $exitCode)) {
            $reasons.Add('A command has missing completion or exit evidence.')
        } elseif ($exitCode -ne 0) { $failed = $true; $reasons.Add('A verified command failed.') }
    }
    $count = Get-WindowsEvidenceValue $Observation FreshExecutableCount
    if (-not (Test-WindowsEvidenceInteger $count) -or $count -lt 1) { $reasons.Add('No fresh test executable was recorded.') }
    if (-not (Test-WindowsEvidenceArray $Observation ObserverErrors)) { $reasons.Add('Observer completion/errors were not recorded as a list.') }
    foreach ($errorEntry in @(Get-WindowsEvidenceValue $Observation ObserverErrors)) {
        if ($errorEntry) { $reasons.Add([string]$errorEntry) }
    }
    foreach ($name in @('BeforeRecordId', 'AfterRecordId', 'OldestRecordId', 'QueryUserEventCount')) {
        if (-not (Test-WindowsEvidenceInteger (Get-WindowsEvidenceValue $Observation $name))) { $reasons.Add("Observation $name is missing or invalid.") }
    }
    $before = Get-WindowsEvidenceValue $Observation BeforeRecordId
    $after = Get-WindowsEvidenceValue $Observation AfterRecordId
    $oldest = Get-WindowsEvidenceValue $Observation OldestRecordId
    if ((Test-WindowsEvidenceInteger $before) -and (Test-WindowsEvidenceInteger $after) -and (Test-WindowsEvidenceInteger $oldest)) {
        if (($after -eq 0 -and $oldest -ne 0) -or ($after -gt 0 -and ($oldest -lt 1 -or $oldest -gt $after))) {
            $reasons.Add('Firewall event bounds were inconsistent.')
        } elseif ($after -lt $before -or ($before -gt 0 -and $oldest -gt $before) -or ($before -eq 0 -and $oldest -gt 1)) {
            $reasons.Add('Firewall event continuity was lost through reset or rollover.')
        }
    }
    $queries = Get-WindowsEvidenceValue $Observation QueryUserEventCount
    if ((Test-WindowsEvidenceInteger $queries) -and $queries -gt 0) { $failed = $true; $reasons.Add('Attributed test executables produced firewall Query User events.') }
    if ($RequireCleanProfile) {
        $sid = Get-WindowsEvidenceValue $Observation ExpectedUserSid
        if (-not ($sid -is [string]) -or [string]::IsNullOrWhiteSpace($sid) -or $sid -ne (Get-WindowsEvidenceValue $Environment UserSid)) { $reasons.Add('The token does not match the newly created disposable user.') }
        foreach ($name in @('ProfileWasAbsent', 'PositiveControlDetected')) {
            $value = Get-WindowsEvidenceValue $Observation $name
            if ($value -isnot [bool] -or -not $value) { $reasons.Add("Clean-profile proof $name was not established.") }
        }
    }
    [pscustomobject]@{
        Accepted = $reasons.Count -eq 0
        Disposition = if ($failed) { 'failed' } elseif ($reasons.Count) { 'incomplete' } elseif ($RequireCleanProfile) { 'accepted-clean-profile' } else { 'accepted-current-profile' }
        Scope = 'native-firewall-listener-regression'
        Reasons = @($reasons)
        Warnings = @($warnings)
    }
}
