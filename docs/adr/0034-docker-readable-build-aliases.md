# ADR 0034: Retroactive human-readable immutable Docker build aliases

Status: Accepted

## Context

The Docker Hub repository `atharvnaphade/carbonpanel` exposes 24 tags: 19
immutable `sha-<commit>` snapshots plus `latest`, `edge`, `nightly`, `1.0` and
`1.0.40`. The sha names are immutable but hard to discuss. A readable alias was
requested retroactively for all existing sha images and for later builds,
without inventing a release, calling historical builds nightly, or disturbing
any existing tag.

A read-only inventory established 19 distinct manifest digests, zero sha-to-sha
collision groups and zero same-second collisions. Docker Hub `last_updated` was
observed to equal `tag_last_pushed` for all 24 tags; it is a publication proxy,
not a verified image build time.

## Decision

Add one immutable alias per distinct source digest, named
`build-<YYYYMMDDTHHMMSSZ>-<SHORT12>`, where the timestamp is the observed Hub
`last_updated` truncated to whole UTC seconds and `SHORT12` is the first 12
hexadecimal characters of the commit encoded in the original sha tag name. The
`build-` prefix deliberately cannot be read as a semver release or a moving
channel.

Collision handling is deterministic: several sha tags for one digest yield one
canonical alias (earliest timestamp, tie-break tag name ascending) with the
other sha refs retained; distinct digests sharing a base alias append the first
8 hex of the manifest digest to every member, escalating to the full digest if
needed. The checked-in mapping at `docs/docker-readable-aliases.json` is
recomputable from its `original_sha_tags` by
`scripts/docker_readable_aliases.py`.

Aliases are added only by an explicit trusted-main `workflow_dispatch` with
`confirm=true`, after read-only exact-commit main CI verification. The workflow
strictly observes each source sha ref and target alias: a definitive missing
alias may be created, a matching alias is idempotent, and a conflicting digest
or unknown/ambiguous lookup refuses before any write. Writes use only
`docker buildx imagetools create --tag <alias> <repo>@<digest>`: no rebuild, no
layer push, no platform change. Original sha refs and the five channel/release
aliases are never written.

## Consequences

The mapping is honest about provenance: the timestamp is tag publication time,
the commit id comes from the tag name rather than a git checkout, and a Hub
digest is the OCI index digest (attestation descriptors are not runnable
platforms). Every alias is pinned to an exact existing digest, so rollback is
unaffected. The actual backfill is a separate authorized step after reviewed
merge and verified main CI; no registry write occurs in this change.
