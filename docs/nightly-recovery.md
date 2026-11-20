# Recover a split Nightly publication

Use this procedure when Docker Hub serves a qualified Nightly image, GHCR has
no Nightly alias, and public Nightly docs advertise a separate qualified
revision. The procedure records those three observations independently. Once
both aliases and the canonical docs agree, use the normal channel workflow.

## Prepare the immutable inputs

1. Confirm the exact public Hub index, both platform configs and the
   matching `sha-<commit>` tag. Locate its successful main CI and CD build.
   Confirm that GHCR responds with `MANIFEST_UNKNOWN` for `nightly` and read
   both public copies of `version-index.json`. Unavailable or conflicting
   observations stop the procedure.
2. Merge the reviewed workflow and select the exact current main revision
   after its push CI succeeds. The lead must qualify the candidate
   `sha-<commit>` at the same index digest in both registries before the
   recovery can proceed. Use a trusted main CD build to produce the Hub
   immutable image if absent. For a missing GHCR SHA tag, mirror the exact
   Hub index and all child manifests, including its attestations. For example,
   run `skopeo copy --all --preserve-digests` from
   `docker://atharvnaphade/carbonpanel@<digest>` to
   `docker://ghcr.io/athndev/carbonpanel:sha-<commit>` after checking that
   the destination tag is definitively absent. Reuse a verified matching tag
   and stop on any conflicting or unknown tag. The recovery workflow reads
   immutable references and writes only the Nightly aliases and Pages.
3. Quiesce alias and Pages writers for the recovery window. Set
   `NIGHTLY_SCHEDULE_PUBLISH` and `CHANNEL_RECONCILE_PUBLISH` to a value other
   than `true`, set `NIGHTLY_RECOVERY_ACTIVE=true`, and let in-flight writes
   finish. Pause and drain automatic docs-only publication as well as Stable
   and other Pages writes for the full window. The shared `pages-deploy` lock
   serializes each phase and is released between dispatches. Select main and
   leave the developer `push` and `nightly` inputs off.
4. Dispatch CD with `recovery=prepare` and `recovery_evidence` in this shape:

   ~~~json
   {
     "hub": {"commit":"<sha>","digest":"sha256:<digest>","build_run":123},
     "docs": {"commit":"<sha>","digest":"sha256:<digest>"},
     "candidate": {"commit":"<sha>","digest":"sha256:<digest>","build_run":456}
   }
   ~~~

   Prepare validates the BuildKit source and subject for each platform,
   corroborates the immutable push from the trusted main CD logs, and checks
   exact main CI for the Hub baseline and candidate. It saves the exact prior
   deployed Pages tree and builds the staged and final trees. Both builds
   receive the Stable tag, revision, and digest verified from the public docs
   and the two registries. The legacy absence of both `stable` aliases is
   recorded when both `latest` aliases independently agree with the docs.
   A partial or conflicting Stable alias pair stops recovery.
   For the historical `v1.0.40` tag, the renderer only accepts its checked-in
   source, so the workflow first requires that the exact tracked tag, revision,
   digest, and version equal the verified public Stable identity.
5. Review the `nightly-recovery-plan` artifact and its SHA-256 from the run
   summary. The plan binds all three Pages trees and hashes for the two
   canonical indexes, marker, Stable and Nightly HTML and OpenAPI, and the
   off-path candidate. Keep the artifact for the full recovery window. If the
   prior deployed artifact is unavailable or its bytes differ, stop before
   publication.

## Advance and verify

For every remaining dispatch use the reviewed plan binding in
`recovery_evidence`, for example `{"run":"123","digest":"sha256:<plan-digest>"}`.
Use the same binding for inspection, retries, and compensation. Wait for
the receipt after each phase.

| Phase | Required observed state | Verified result |
| --- | --- | --- |
| `stage` | Original Hub, absent GHCR and original Pages | Deploy the original site with only the off-path candidate subtree added. Canonical HTML, OpenAPI and index bytes still match the prior artifact. |
| `hub` | Original Hub, absent GHCR and verified staged Pages | Move Hub `nightly` to the candidate index by digest. |
| `docs` | Candidate Hub, absent GHCR and verified staged Pages | Deploy final canonical Nightly docs and OpenAPI. Verify the pinned Stable identity and both channels' public probes. |
| `commit` | Candidate Hub, absent GHCR and verified final Pages | Create GHCR `nightly` from the identical candidate index, verify both aliases and all public probes, and retain the final receipt. |

Each phase compares live aliases and public bytes immediately before its
one permitted write. A successful phase retried from its observed post-state
emits a receipt without repeating the write. Recheck the candidate's main
tip and CI before every forward mutation. If main advances during recovery,
use the same plan to inspect or compensate.

## Resume or compensate

Use `inspect` with the original plan after a failed or interrupted run. If
the phase's pre-state or post-state is intact, retry that phase. For an
asynchronous Pages submission, the previous deploy must have reached a
definitive provider state before another mutation. If an interrupted run
has no identifiable Pages deployment, have the release lead resolve it
with the provider before proceeding. Mixed Pages bytes cannot advance the
promotion. A settled failed deployment can be restored when every probed route
matches a known plan value. Unknown bytes, unknown lookups and unrelated tag
moves stop further writes.

- After `stage`, run `restore-docs` to deploy the exact prior Pages tree.
- After `hub` or `docs`, run `restore-hub`, then `restore-docs`. The Hub
  source is the recorded prior digest, and the exact prior site comes from the
  reviewed plan. Verify original Hub, absent GHCR and the original docs bytes.
- Once the GHCR alias is observed at the candidate digest, finish verification
  with `commit` or `inspect`. This is the commit point. Deleting the GHCR
  package version could also delete the immutable SHA tag. Keep the candidate
  digest and resolve any read or verification failure without deleting it.

Record the final or compensated receipt in OSS-413. Restore normal
publication controls after the lead accepts the observed result. The only
successful promotion receipt is one with `verified_final: true`, two
observed candidate aliases and verified final Pages probes.
