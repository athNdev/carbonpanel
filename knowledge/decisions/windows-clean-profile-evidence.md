---
type: Decision
title: Clean Windows profile evidence
description: Clean-profile firewall qualification requires a new standard-user SID and an independently observed positive control; missing proof is incomplete.
status: draft
owners: [oss-runtime]
code_refs:
  - .github/workflows/windows-clean-profile.yml
  - scripts/windows-clean-profile-ci.ps1
  - scripts/test-windows-clean-profile-ci.ps1
  - scripts/testdata/windows-firewall-control
  - scripts/windows-verification-evidence.ps1
sources:
  - title: Native Windows verification procedure
    resource: ../../docs/testing/windows-verification.md
  - title: Hosted runner isolation and UAC policy
    resource: https://docs.github.com/en/actions/reference/runners/github-hosted-runners
---

# Choice

Qualify a clean profile with a newly created standard-user SID and loaded
profile, enabled effective firewall policy and an independently observed public
listener control. The child supplies its token facts; the administrator observer
supplies read-only machine-policy and event facts with explicit provenance.

Event 2097 alone is a generic rule event. Query User evidence requires the
native TCP/UDP Query User rule identifier and an owned `ApplicationPath`.
Record the rule identifier, action and application path for review; numeric
action values and `ModifyingApplication` do not classify consent events.

# Alternatives

A passing inherited runner-administrator session cannot establish ordinary-user
behavior. Changing temporary directories does not create a Windows profile.
Zero events without a functioning positive control cannot establish absence of
consent events. These cases remain incomplete rather than accepted.

# Consequences

Account and firewall setup run only in explicitly opted-in disposable hosted
VMs. Ordinary developer verification remains read-only. Command or attributed
test-event failures fail qualification; missing facts or a missing control are
incomplete. Existing hosted UAC policy is recorded, not changed; disabled UAC
cannot establish secure-desktop dialog behavior. Artifacts contain sanitized
facts and command logs, never the profile or credential object.

Timeout cleanup requests termination of the owned child tree, but only proves
the root process exited. The disposable VM's retirement is the final boundary;
this is not the development supervisor's durable job-ownership guarantee.
