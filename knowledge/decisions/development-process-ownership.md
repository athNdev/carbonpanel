---
type: Decision
title: Development process ownership
description: Windows development commands enter native jobs before execution, while POSIX commands use owned process groups.
status: stable
owners: [oss-runtime]
code_refs:
  - cmd/carbon-dev-supervisor
  - scripts/dev-processes.ts
  - scripts/dev.test.ts
  - scripts/testdata/dev-process
sources:
  - title: Development commands
    resource: ../../docs/development.md
---

# Choice

Each Windows development command starts suspended inside a native helper. The
creation attribute list assigns it to a Job Object atomically; a later
create-then-assign sequence could strand a suspended root if the helper died
between those steps. The helper resumes the command and alone owns the
non-inherited kill-on-close handle. It retains the actual launcher process
handle and rejects a parent PID reused after helper creation. Root exit, helper
death and launcher death therefore end that command's descendants. A job
assignment error refuses execution. POSIX retains its process-group policy.

# Alternatives

Live-PID tree cleanup loses descendants after their parent exits; process-name
cleanup can kill another checkout. JavaScript-only child fixtures can mask the
gap through runtime cleanup, so tests use native parent/descendant processes.

# Consequences

The helper is an ordinary local developer tool built from checked-in Go source
into a content-keyed `tmp` executable. Initial compilation precedes native job
availability: normal stop terminates that live tree, but abrupt launcher death
during bootstrap is not covered by the job guarantee. Ordinary commands start
only after preparation. Stdio is inherited explicitly; the job handle is not.
An existing console is attached without opening a window. The command uses its
own console process group, while the helper retains ownership during Ctrl+C so
the JavaScript owner stops all commands together. Hidden-terminal tests exercise
keyboard input, TTY identity and the real console interrupt path.
No elevation, host security-policy change or persistent-data cleanup is needed.
