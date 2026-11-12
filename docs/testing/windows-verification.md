# Native Windows verification without recurring prompts

Ordinary package tests run in a standard, unelevated shell. They use loopback
sockets and temporary stores. They do not need a firewall exception, a UAC
policy change, an elevated terminal, or a rule for temporary test executables.

## Socket ownership

`proxy.bind_host` controls manager-created Minecraft, module and direct-knock
ingress. Its empty production default keeps public listeners available. Local
development sets `CARBONPANEL_PROXY_BIND_HOST=127.0.0.1`; the controller and
frontend also need their explicit local-development hosts. Use the named LAN
development profile only when other machines should connect.

Tests that construct a proxy manager use `NewManagerWithListeners` with an
instance-owned `ListenerFactory`. The test adapter rejects unspecified and
non-loopback TCP/UDP addresses before calling the operating system. Route-only
fixtures use ephemeral ports; real TCP/UDP protocol tests remain in the suite.
Backend UDP session sockets have a separate instance-owned factory, so a test
can retain real sockets and verify race-loser cleanup without opening a wildcard
socket. Production backend routing keeps its independent source-address choice.

Do not add mutable package-global listener hooks or silently skip native Windows
tests. An apparently local test may start a public listener indirectly through a
manager, module route or background worker; inspect the actual constructor path.

## Verify rebuilt executables

Run the focused packages, then the complete suite, using fresh uncached test
executables and normal package parallelism:

```powershell
go test -count=1 ./internal/config ./internal/proxy ./internal/module
go test -count=1 ./...
go vet ./...
```

Repeat the full run and normal frontend/build/development commands. Check the
actual bound interfaces during development. A successful test exit alone does
not prove that Windows avoided asking for approval.

Where existing Windows Firewall logs are readable, compare new records around
the run in `Microsoft-Windows-Windows Firewall With Advanced Security/Firewall`.
Query User rules for fresh `.test.exe` paths indicate that an unintended network
listener still escaped the fixture policy. Do not enable/change security policy
or remove existing rules as a side effect of the test command. A clean disposable
Windows profile is useful for proving that earlier approvals do not mask a
recurrence.

## Record the qualification environment

The read-only helper records the current token, UAC setting, effective firewall
profiles and event-channel availability. It does not enable logging, remove
rules, create accounts or request elevation:

```powershell
. ./scripts/windows-verification-evidence.ps1
Get-WindowsVerificationEnvironment | ConvertTo-Json -Depth 8
```

Keep raw environment and command records outside tracked source. Record the
source revision, completed commands and exit codes, distinct rebuilt executable
paths, event-log bounds, attributed Query User events, and any observer errors.
An observer must start before the commands and finish after them; log reset or
rollover makes the interval incomplete. Successful commands alone do not prove
that consent was avoided.

`Test-WindowsVerificationEvidence` evaluates those records without running the
commands. Missing token, policy, command, fresh-binary, observer or event-range
evidence produces `incomplete`; a failed command or attributed Query User event
produces `failed`. A complete record can be accepted only for its stated
`native-firewall-listener-regression` scope. The pure policy fixtures run with:

```powershell
pwsh -NoProfile -File scripts/test-windows-verification-evidence.ps1
```

Pure fixtures establish record classification, not native Windows acceptance.

## Qualify a genuinely clean profile

Use a disposable Windows environment with a newly created standard user and
effective firewall profiles that enable listen notifications and block default
ingress. A new `TEMP`, `APPDATA` or checkout directory is not a new Windows
profile. Capture that the profile was absent before loading it and that the
test process token matches the expected new user SID.

Before accepting clean-profile evidence, an intentionally public listener in
that disposable environment must generate an attributable Query User event.
This positive control establishes that the event observer can detect the
failure being checked. Run it only through the separately named CI qualification
setup, never as part of normal tests or on a developer workstation. Missing
positive-control evidence leaves qualification incomplete.

A privileged observer may collect machine policy and event facts separately
from the standard test process. Keep both records and identify their sources;
the privileged observer's token is not the token that ran the tests.
[GitHub documents administrator sessions and disabled UAC as hosted Windows defaults](https://docs.github.com/en/actions/reference/runners/github-hosted-runners#administrative-privileges),
so an ordinary green test job is not standard-user qualification. When UAC is disabled,
the evaluator emits a warning and makes no secure-desktop UAC-dialog claim.
Record current-profile and clean-profile results separately; neither silently
substitutes for the other.

## Intentional LAN setup

Intentional public/LAN or privileged infrastructure setup is separate from
ordinary verification. If Windows configuration is demonstrably necessary,
perform it through one deliberate setup operation, with at most one initial
approval and only the stable application paths, protocols and scope required.
Never broadly allow temporary executables, disable UAC/firewall, or elevate each
test run.
