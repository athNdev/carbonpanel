# SPDX-License-Identifier: MPL-2.0
# This setup is confined to a disposable hosted VM. Normal Windows verification
# uses windows-verification-evidence.ps1 without changing accounts or policy.
[CmdletBinding()]
param(
    [ValidateSet('Parent', 'Child')][string]$Mode = 'Parent',
    [ValidateSet('oss', 'cloud')][string]$ProjectKind = 'oss',
    [string]$ConfigurationPath,
    [switch]$AllowDisposableRunnerSetup
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Assert-DisposableWindowsRunner {
    if ($env:OS -ne 'Windows_NT' -or $env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') {
        throw 'Clean-profile setup requires an explicitly opted-in disposable GitHub-hosted Windows VM.'
    }
    if ([string]::IsNullOrWhiteSpace($env:GITHUB_WORKSPACE) -or [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP)) {
        throw 'Hosted workspace and temporary roots are required.'
    }
}

# This guard precedes every path that can create an account, alter the disposable
# VM's firewall or run the deliberately public control.
Assert-DisposableWindowsRunner
if ($Mode -eq 'Parent' -and -not $AllowDisposableRunnerSetup) {
    throw 'Pass -AllowDisposableRunnerSetup only in the disposable qualification workflow.'
}

function Write-QualificationJson {
    param([object]$Value, [string]$Path)
    ConvertTo-Json -InputObject $Value -Depth 12 | Set-Content -LiteralPath $Path -Encoding utf8
}

function Test-PathInside {
    param([string]$Path, [string]$Root)
    $full = [IO.Path]::GetFullPath($Path)
    $parent = [IO.Path]::GetFullPath($Root).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    $full.StartsWith($parent, [StringComparison]::OrdinalIgnoreCase)
}

function Invoke-ChildCommand {
    param([string]$Executable, [string[]]$Arguments, [string]$LogPath, [string]$Identity)
    & $Executable @Arguments *> $LogPath
    $status = $LASTEXITCODE
    [pscustomobject]@{ Command = $Identity; Completed = $true; ExitCode = [int]$status }
}

if ($Mode -eq 'Child') {
    if (-not (Test-PathInside $ConfigurationPath $env:RUNNER_TEMP)) { throw 'Child configuration is outside the hosted temporary root.' }
    $configuration = Get-Content -LiteralPath $ConfigurationPath -Raw | ConvertFrom-Json
    $work = [string]$configuration.WorkRoot
    $repo = [IO.Path]::GetFullPath([string]$configuration.Repository)
    if (-not (Test-PathInside $work $env:RUNNER_TEMP) -or $repo -ne [IO.Path]::GetFullPath($env:GITHUB_WORKSPACE)) {
        throw 'Child roots do not match the disposable workspace.'
    }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $isAdmin = ([Security.Principal.WindowsPrincipal]$identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    $token = [pscustomobject]@{
        IsWindows = $env:OS -eq 'Windows_NT'
        UserSid = $identity.User.Value
        IsAdministrator = $isAdmin
        ProfileLoaded = Test-Path -LiteralPath ('Registry::HKEY_USERS\' + $identity.User.Value)
    }
    Write-QualificationJson $token (Join-Path $work ('token-' + $configuration.Phase + '.json'))
    if ($isAdmin -or $identity.User.Value -ne $configuration.ExpectedUserSid -or -not $token.ProfileLoaded) {
        throw 'Child did not enter the newly created standard-user profile.'
    }

    # Alternate credentials load a genuine Windows profile. Give the Go tools
    # explicit private caches rather than inheriting the runner admin's home.
    $profile = Get-ItemPropertyValue -LiteralPath ('HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\' + $identity.User.Value) -Name ProfileImagePath
    $profilePath = [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($profile))
    if ($profilePath -eq [IO.Path]::GetFullPath([string]$configuration.RunnerProfilePath) -or -not (Test-Path -LiteralPath $profilePath)) {
        throw 'The disposable token did not receive its own registered profile directory.'
    }
    # Alternate-credential starts may inherit environment values. Keep only
    # required machine/runner identity markers, then construct tool/home/cache
    # paths explicitly. No Actions service token reaches the tested process.
    $keptVariables = @('OS', 'SystemRoot', 'WINDIR', 'SYSTEMDRIVE', 'COMSPEC', 'PATHEXT', 'ProgramFiles', 'ProgramFiles(x86)', 'ProgramW6432', 'PROCESSOR_ARCHITECTURE', 'PROCESSOR_IDENTIFIER', 'NUMBER_OF_PROCESSORS', 'COMPUTERNAME', 'GITHUB_ACTIONS', 'RUNNER_ENVIRONMENT', 'RUNNER_TEMP', 'GITHUB_WORKSPACE')
    foreach ($variable in @([Environment]::GetEnvironmentVariables('Process').Keys)) {
        if ($variable -notin $keptVariables) { [Environment]::SetEnvironmentVariable([string]$variable, $null, 'Process') }
    }
    $env:PSModulePath = @((Join-Path $PSHOME 'Modules'), (Join-Path $env:ProgramFiles 'WindowsPowerShell\Modules'), (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\Modules')) -join ';'
    $env:USERPROFILE = $profilePath
    $env:USERNAME = ($identity.Name -split '\\')[-1]
    $env:USERDOMAIN = $env:COMPUTERNAME
    $env:APPDATA = Join-Path $env:USERPROFILE 'AppData\Roaming'
    $env:LOCALAPPDATA = Join-Path $env:USERPROFILE 'AppData\Local'
    $env:HOMEDRIVE = [IO.Path]::GetPathRoot($profilePath).TrimEnd('\')
    $env:HOMEPATH = $profilePath.Substring($env:HOMEDRIVE.Length)
    $env:TEMP = Join-Path $work 'user-temp'
    $env:TMP = $env:TEMP
    [void](New-Item -ItemType Directory -Path $env:TEMP -Force)
    $machinePath = @([Environment]::GetEnvironmentVariable('PATH', 'Machine') -split ';' | Where-Object { $_ -and -not (Test-PathInside $_ $configuration.RunnerProfilePath) })
    $env:PATH = @((Join-Path $work 'tools'), (Split-Path $configuration.GoExecutable -Parent), (Split-Path $configuration.GitExecutable -Parent), $PSHOME) + $machinePath -join ';'
    $env:GOPATH = Join-Path $work 'go-path'
    $env:GOCACHE = Join-Path $work 'go-cache'
    $env:GOMODCACHE = Join-Path $work 'go-modules'
    $env:GOENV = 'off'
    $env:GOTOOLCHAIN = 'local'
    $env:GIT_CONFIG_NOSYSTEM = '1'
    $env:GIT_CONFIG_GLOBAL = Join-Path $work 'gitconfig'
    # The checkout belongs to the runner administrator. Trust only this exact
    # repository in the new user's private config, never a global wildcard.
    & $configuration.GitExecutable config --global --add safe.directory $repo
    if ($LASTEXITCODE -ne 0) { throw "Could not set the disposable user's exact repository trust entry." }
    $env:DOCKER_HOST = 'npipe:////./pipe/carbon-clean-profile-no-daemon-' + $configuration.RunId
    $token | Add-Member -NotePropertyName ProfilePath -NotePropertyValue $profilePath
    Write-QualificationJson $token (Join-Path $work ('token-' + $configuration.Phase + '.json'))
    Set-Location -LiteralPath $repo
    if ($configuration.Phase -eq 'control') {
        $env:CARBON_WINDOWS_POSITIVE_CONTROL = '1'
        $env:CARBON_WINDOWS_CONTROL_USER_SID = $identity.User.Value
        & $configuration.ControlExecutable '--allow-disposable-public-listener'
        exit $LASTEXITCODE
    }

    $commands = [Collections.Generic.List[object]]::new()
    foreach ($command in @(
        @{ Name = 'go build ./...'; Arguments = @('build', './...'); Log = 'go-build.log' },
        @{ Name = 'go vet ./...'; Arguments = @('vet', './...'); Log = 'go-vet.log' }
    )) {
        $result = Invoke-ChildCommand $configuration.GoExecutable $command.Arguments (Join-Path $work $command.Log) $command.Name
        $commands.Add($result)
        Write-QualificationJson ($commands.ToArray()) (Join-Path $work 'commands.json')
        if ($result.ExitCode -ne 0) { exit $result.ExitCode }
    }
    foreach ($run in 1..2) {
        $env:GOTMPDIR = Join-Path $work ('go-tmp\run-' + $run)
        [void](New-Item -ItemType Directory -Path $env:GOTMPDIR -Force)
        $result = Invoke-ChildCommand $configuration.GoExecutable @('test', '-work', '-count=1', '-timeout=5m', './...') (Join-Path $work ('go-test-' + $run + '.log')) ('go test ./... uncached run ' + $run)
        $commands.Add($result)
        Write-QualificationJson ($commands.ToArray()) (Join-Path $work 'commands.json')
        if ($result.ExitCode -ne 0) { exit $result.ExitCode }
    }
    if ($configuration.ProjectKind -eq 'oss') {
        $result = Invoke-ChildCommand $configuration.BunExecutable @('test', 'scripts/dev.test.ts') (Join-Path $work 'bun-dev-test.log') 'bun test scripts/dev.test.ts'
        $commands.Add($result)
        Write-QualificationJson ($commands.ToArray()) (Join-Path $work 'commands.json')
        if ($result.ExitCode -ne 0) { exit $result.ExitCode }
    }
    exit 0
}

. "$PSScriptRoot/windows-verification-evidence.ps1"
$repository = [IO.Path]::GetFullPath($env:GITHUB_WORKSPACE)
$temporaryRoot = [IO.Path]::GetFullPath($env:RUNNER_TEMP)
$runId = [guid]::NewGuid().ToString('N')
$work = Join-Path $temporaryRoot ('carbon-profile-' + $runId)
$artifacts = Join-Path $repository 'tmp\windows-clean-profile-evidence'
if (-not (Test-PathInside $work $temporaryRoot) -or -not (Test-PathInside $artifacts $repository)) { throw 'Invalid qualification output roots.' }
[void](New-Item -ItemType Directory -Path $work, $artifacts -Force)
$channel = 'Microsoft-Windows-Windows Firewall With Advanced Security/Firewall'
$observerErrors = [Collections.Generic.List[string]]::new()
$assessment = $null
$standardUser = $null
$activeChild = $null

function Convert-ProgramPath {
    param([string]$Path)
    $path = $Path.Replace('/', '\')
    if ($path.StartsWith('\??\') -or $path.StartsWith('\\?\')) { $path = $path.Substring(4) }
    $path.TrimEnd('\')
}

function Test-ProgramPathMatch {
    param([string]$ApplicationPath, [string[]]$Aliases, [switch]$Descendants)
    if ([string]::IsNullOrWhiteSpace($ApplicationPath)) { return $false }
    $actual = Convert-ProgramPath $ApplicationPath
    foreach ($alias in $Aliases) {
        $expected = Convert-ProgramPath $alias
        if ($Descendants) {
            if ($actual.StartsWith(($expected + '\'), [StringComparison]::OrdinalIgnoreCase)) { return $true }
        } elseif ($actual.Equals($expected, [StringComparison]::OrdinalIgnoreCase)) { return $true }
    }
    $false
}

function Get-ProgramPathAliases {
    param([string]$Path)
    $full = [IO.Path]::GetFullPath($Path)
    if ($full -notmatch '^[A-Za-z]:\\') { throw 'Qualification event attribution requires a local drive path.' }
    if (-not ('Carbon.WindowsQualification.DosPaths' -as [type])) {
        Add-Type -TypeDefinition @'
using System.Runtime.InteropServices;
using System.Text;
namespace Carbon.WindowsQualification {
    public static class DosPaths {
        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        public static extern uint QueryDosDevice(string device, StringBuilder path, int length);
    }
}
'@
    }
    $target = [Text.StringBuilder]::new(32768)
    if (-not [Carbon.WindowsQualification.DosPaths]::QueryDosDevice($full.Substring(0, 2), $target, $target.Capacity)) {
        throw 'Could not resolve the event-log device path for this qualification root.'
    }
    @($full, (($target.ToString() -split [char]0)[0] + $full.Substring(2)))
}

function Get-FirewallApplicationPath {
    param([object]$Event)
    [xml]$xml = $Event.ToXml()
    $paths = $xml.SelectNodes("/*[local-name()='Event']/*[local-name()='EventData']/*[local-name()='Data' and @Name='ApplicationPath']")
    if ($paths.Count -eq 1) { return [string]$paths[0].InnerText }
    ''
}

function Get-AttributedQueries {
    param([long]$After, [string[]]$Aliases, [switch]$Descendants, [string[]]$ExcludeAliases = @())
    $events = @()
    try {
        $events = @(Get-WinEvent -LogName $channel -FilterXPath ("*[System[(EventID=2097) and (EventRecordID > {0})]]" -f $After) -ErrorAction Stop)
    } catch {
        if ($_.FullyQualifiedErrorId -notmatch '^NoMatchingEventsFound') { throw }
    }
    @($events | Where-Object {
        $application = Get-FirewallApplicationPath $_
        $application.EndsWith('.exe', [StringComparison]::OrdinalIgnoreCase) -and
            (Test-ProgramPathMatch $application $Aliases -Descendants:$Descendants) -and
            -not (Test-ProgramPathMatch $application $ExcludeAliases)
    })
}

function Invoke-StandardPhase {
    param([string]$Phase, [int]$TimeoutSeconds)
    $config = [ordered]@{
        WorkRoot = $work; Repository = $repository; RunId = $runId
        GoExecutable = $goExecutable; GitExecutable = $gitExecutable; BunExecutable = $bunExecutable; ControlExecutable = $controlExecutable
        RunnerProfilePath = $env:USERPROFILE
        ExpectedUserSid = $standardUser.SID.Value; Phase = $Phase; ProjectKind = $ProjectKind
    }
    $configPath = Join-Path $work ($Phase + '-configuration.json')
    Write-QualificationJson $config $configPath
    $arguments = @('-NoLogo', '-NoProfile', '-NonInteractive', '-File', ('"' + $PSCommandPath + '"'), '-Mode', 'Child', '-ConfigurationPath', ('"' + $configPath + '"'))
    $script:activeChild = Start-Process -FilePath $pwshExecutable -ArgumentList $arguments -WorkingDirectory $repository -Credential $credential -LoadUserProfile -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $work ($Phase + '-stdout.log')) -RedirectStandardError (Join-Path $work ($Phase + '-stderr.log'))
    # Retain the native handle before exit so a reused PID cannot become the
    # timeout target, and so ExitCode remains readable after WaitForExit.
    $null = $script:activeChild.Handle
    if (-not $script:activeChild.WaitForExit($TimeoutSeconds * 1000)) {
        $script:activeChild.Kill($true)
        $script:activeChild.WaitForExit()
        throw "$Phase exceeded its bounded qualification timeout."
    }
    $status = $script:activeChild.ExitCode
    $script:activeChild.Dispose()
    $script:activeChild = $null
    [int]$status
}

try {
    $observerBefore = Get-WindowsVerificationEnvironment
    Write-QualificationJson $observerBefore (Join-Path $artifacts 'observer-before-setup.json')
    if (-not $observerBefore.IsAdministrator) { throw 'Disposable VM setup requires its existing runner administrator token.' }
    $goExecutable = (Get-Command go -CommandType Application | Select-Object -First 1).Source
    $gitExecutable = (Get-Command git -CommandType Application | Select-Object -First 1).Source
    $bunExecutable = $null
    $pwshExecutable = Join-Path $PSHOME 'pwsh.exe'
    $controlExecutable = Join-Path $work ('public-control-' + $runId + '.exe')
    $module = Get-Content -LiteralPath (Join-Path $repository 'go.mod') | Where-Object { $_ -match '^module\s+' } | Select-Object -First 1
    $expectedModule = if ($ProjectKind -eq 'oss') { 'module github.com/athNdev/carbon-panel' } else { 'module github.com/athNdev/carbon-cloud' }
    if ($module.Trim() -ne $expectedModule) { throw 'Project kind does not match the checked-out Go module.' }
    if ($ProjectKind -eq 'oss') {
        [void](New-Item -ItemType Directory -Path (Join-Path $work 'tools') -Force)
        $bunExecutable = Join-Path $work 'tools\bun.exe'
        Copy-Item -LiteralPath (Get-Command bun -CommandType Application | Select-Object -First 1).Source -Destination $bunExecutable
    }
    Write-QualificationJson ([pscustomobject]@{
        SourceCommit = (& git -C $repository rev-parse HEAD)
        QualificationToolCommit = (& git -C (Split-Path $PSScriptRoot -Parent) rev-parse HEAD)
        ProjectKind = $ProjectKind
        GoVersion = (& $goExecutable version)
        OperatingSystem = [Runtime.InteropServices.RuntimeInformation]::OSDescription
        Architecture = [Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture.ToString()
        RunnerImage = $env:ImageOS
        RunnerImageVersion = $env:ImageVersion
    }) (Join-Path $artifacts 'run.json')
    if ($ProjectKind -eq 'oss') {
        [void](New-Item -ItemType Directory -Path (Join-Path $repository 'web\carbon-panel\build') -Force)
        [IO.File]::WriteAllText((Join-Path $repository 'web\carbon-panel\build\.gitkeep'), '')
    }
    & $goExecutable -C (Split-Path $PSScriptRoot -Parent) build -o $controlExecutable ./scripts/testdata/windows-firewall-control
    if ($LASTEXITCODE -ne 0) { throw 'Could not build the guarded positive control.' }

    $name = 'cpq' + $runId.Substring(0, 12)
    $password = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32)) + 'Aa1!'
    $securePassword = ConvertTo-SecureString $password -AsPlainText -Force
    $password = $null
    $standardUser = New-LocalUser -Name $name -Password $securePassword -AccountNeverExpires -PasswordNeverExpires -UserMayNotChangePassword -Description 'Disposable CI qualification profile'
    if (-not @((Get-LocalGroupMember -SID 'S-1-5-32-545') | Where-Object { $_.SID.Value -eq $standardUser.SID.Value }).Count) {
        Add-LocalGroupMember -SID 'S-1-5-32-545' -Member $standardUser
    }
    $profileWasAbsent = @((Get-CimInstance Win32_UserProfile) | Where-Object SID -eq $standardUser.SID.Value).Count -eq 0
    if (-not $profileWasAbsent) { throw 'Disposable standard-user profile was already present.' }
    Write-QualificationJson ([pscustomobject]@{ ExpectedUserSid = $standardUser.SID.Value; ProfileWasAbsent = $profileWasAbsent }) (Join-Path $artifacts 'profile-creation.json')
    $credential = [PSCredential]::new(($env:COMPUTERNAME + '\' + $name), $securePassword)

    # Grant only this new SID access to this job's checkout and private work
    # directory. No runner home, unrelated checkout or retained profile is used.
    foreach ($directory in @($repository, $work)) {
        $acl = Get-Acl -LiteralPath $directory
        $rule = [Security.AccessControl.FileSystemAccessRule]::new($standardUser.SID, 'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
        Set-Acl -LiteralPath $directory -AclObject $acl
    }
    # These changes are confined to the fresh hosted VM, never a developer
    # machine. Existing UAC policy is deliberately not changed.
    Set-NetFirewallProfile -Profile Domain, Private, Public -Enabled True -NotifyOnListen True -DefaultInboundAction Block
    & wevtutil.exe sl $channel /e:true
    if ($LASTEXITCODE -ne 0) { throw 'Could not enable the disposable VM firewall event channel.' }
    $observer = Get-WindowsVerificationEnvironment
    Write-QualificationJson $observer (Join-Path $artifacts 'observer-environment.json')
    $controlBefore = [long]$observer.FirewallLog.LatestRecordId
    $controlAliases = @(Get-ProgramPathAliases $controlExecutable)
    $workAliases = @(Get-ProgramPathAliases $work) + @(Get-ProgramPathAliases (Join-Path $repository 'tmp'))
    $controlExit = Invoke-StandardPhase 'control' 90
    $controlQueries = @()
    foreach ($attempt in 1..10) {
        $controlQueries = @(Get-AttributedQueries -After $controlBefore -Aliases $controlAliases)
        if ($controlQueries.Count) { break }
        Start-Sleep -Seconds 1
    }
    $control = [pscustomobject]@{ ExitCode = $controlExit; QueryUserEventCount = $controlQueries.Count; RecordIds = @($controlQueries | ForEach-Object RecordId) }
    Write-QualificationJson $control (Join-Path $artifacts 'positive-control.json')
    if (Test-Path -LiteralPath (Join-Path $work 'token-control.json')) {
        Copy-Item -LiteralPath (Join-Path $work 'token-control.json') -Destination (Join-Path $artifacts 'control-user-token.json')
    }
    if ($controlExit -ne 0) { throw 'The standard-user positive-control process did not complete successfully; clean-profile qualification is incomplete.' }
    if ($controlQueries.Count -eq 0) { throw 'The public-listener positive control produced no attributable Query User event; this environment cannot establish clean-profile consent evidence.' }

    $beforeTests = Get-WindowsVerificationEnvironment
    $testExit = Invoke-StandardPhase 'tests' 2400
    Start-Sleep -Seconds 1
    $afterTests = Get-WindowsVerificationEnvironment
    $token = Get-Content -LiteralPath (Join-Path $work 'token-tests.json') -Raw | ConvertFrom-Json
    $commands = @()
    if (Test-Path -LiteralPath (Join-Path $work 'commands.json')) {
        $commands = @(Get-Content -LiteralPath (Join-Path $work 'commands.json') -Raw | ConvertFrom-Json)
    } else { $observerErrors.Add('Standard-user process did not record command completion.') }
    if ($testExit -ne 0 -and @($commands | Where-Object ExitCode -ne 0).Count -eq 0) {
        $observerErrors.Add('Standard-user test process failed before recording its command failure.')
    }
    $expectedCommands = @('go build ./...', 'go vet ./...', 'go test ./... uncached run 1', 'go test ./... uncached run 2')
    if ($ProjectKind -eq 'oss') { $expectedCommands += 'bun test scripts/dev.test.ts' }
    if (($commands.Count -ne $expectedCommands.Count) -or (($commands | ForEach-Object Command) -join '|') -ne ($expectedCommands -join '|')) {
        $observerErrors.Add('Not every required build, vet and repeated uncached suite command was recorded in order.')
    }
    $queries = @(Get-AttributedQueries -After ([long]$beforeTests.FirewallLog.LatestRecordId) -Aliases $workAliases -Descendants -ExcludeAliases $controlAliases)
    $executables = @()
    if (Test-Path -LiteralPath (Join-Path $work 'go-tmp')) {
        $executables = @(Get-ChildItem -LiteralPath (Join-Path $work 'go-tmp') -Filter '*.test.exe' -File -Recurse -ErrorAction Stop)
    }
    $environment = [pscustomobject]@{
        CapturedAtUtc = [datetime]::UtcNow.ToString('o')
        IsWindows = $token.IsWindows; UserSid = $token.UserSid; IsAdministrator = $token.IsAdministrator
        EnableLUA = $afterTests.EnableLUA; FirewallProfiles = $afterTests.FirewallProfiles
        FirewallLog = $afterTests.FirewallLog; Errors = @($beforeTests.Errors) + @($afterTests.Errors)
        Provenance = @{ Token = 'standard-user child'; MachinePolicyAndEvents = 'independent runner observer' }
    }
    if (-not $token.ProfileLoaded) { $observerErrors.Add('Standard-user profile hive was not loaded.') }
    $observation = [pscustomobject]@{
        Commands = $commands; ObserverErrors = @($observerErrors)
        FreshExecutableCount = $executables.Count
        BeforeRecordId = [long]$beforeTests.FirewallLog.LatestRecordId
        AfterRecordId = [long]$afterTests.FirewallLog.LatestRecordId
        OldestRecordId = [long]$afterTests.FirewallLog.OldestRecordId
        QueryUserEventCount = $queries.Count
        ExpectedUserSid = $standardUser.SID.Value; ProfileWasAbsent = $profileWasAbsent
        PositiveControlDetected = $controlExit -eq 0 -and $controlQueries.Count -gt 0
    }
    $assessment = Test-WindowsVerificationEvidence -Environment $environment -Observation $observation -RequireCleanProfile
    Write-QualificationJson $token (Join-Path $artifacts 'standard-user-token.json')
    Write-QualificationJson $environment (Join-Path $artifacts 'assessment-environment.json')
    Write-QualificationJson $observation (Join-Path $artifacts 'observation.json')
    Write-QualificationJson $assessment (Join-Path $artifacts 'assessment.json')
    Write-Output ($assessment | ConvertTo-Json -Depth 8)
} catch {
    $assessment = [pscustomobject]@{ Accepted = $false; Disposition = 'incomplete'; Scope = 'native-firewall-listener-regression'; Reasons = @($_.Exception.Message); Warnings = @() }
    Write-QualificationJson $assessment (Join-Path $artifacts 'assessment.json')
    Write-Output ($assessment | ConvertTo-Json -Depth 8)
} finally {
    if ($activeChild) {
        if (-not $activeChild.HasExited) { $activeChild.Kill($true); $activeChild.WaitForExit() }
        $activeChild.Dispose()
    }
    # Only allowlisted text evidence is exported. The random account password,
    # loaded profile, caches and source tree stay inside the discarded VM.
    foreach ($file in @(Get-ChildItem -LiteralPath $work -File -Filter '*.log')) {
        Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $artifacts $file.Name)
    }
    if ($standardUser) { Remove-LocalUser -SID $standardUser.SID }
}
if ($assessment.Accepted) { exit 0 }
if ($assessment.Disposition -eq 'failed') { exit 1 }
exit 2
