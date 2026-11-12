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

Intentional public/LAN or privileged infrastructure setup is separate from
ordinary verification. If Windows configuration is demonstrably necessary,
perform it through one deliberate setup operation, with at most one initial
approval and only the stable application paths, protocols and scope required.
Never broadly allow temporary executables, disable UAC/firewall, or elevate each
test run.
