# Human-readable immutable Docker build aliases

Readable immutable aliases are additions at exact existing Docker Hub manifest digests. No rebuild, no platform change, no channel or release alias move.

## Naming rule

`build-YYYY-MM-DD-HHMMSS[-N]`

- Timestamp: observed Hub `tag_last_pushed` (fallback `last_updated`) truncated to whole UTC seconds.
- This is **tag push / publication time**, NOT a verified image build time; re-pushing a tag can change it.
- No SHA or commit fragment appears in a user-facing alias. Full commit identity stays in the mapping and the retained original `sha-<40hex>` ref.
- Distinct digests sharing a readable base get stable `-<N>` suffixes (`-2`, `-3`, ...), ordered by digest hex.
- One alias per distinct digest; canonical is the earliest publication time (tie-break tag name ascending), other sha refs retained.

## Provenance limits

- last_updated is the Docker Hub tag record last-updated/publication timestamp, NOT a verified image-build time. It can change when tag metadata is edited or when a tag is re-pointed, so it is only a historical provenance proxy.
- Verified: for all 24 observed tags, Hub API last_updated equals tag_last_pushed. The timestamp is therefore the tag push time (not merely a metadata edit), but it is still a publication proxy rather than the original image build time, because a tag can be re-pushed later.
- Observed last_updated for sha-d8597d4e... (07:25:53Z) and the moving "nightly" tag (07:26:45Z) share digest sha256:ecc89ba9...; likewise sha-c15b8d7d... and "edge" share sha256:4c772558.... The close timestamps are consistent with publication time, not necessarily original compile time.
- The Hub images[] list exposes 2 runnable platforms (linux/amd64, linux/arm64). OCI inspection shows an additional attestation manifest (unknown/unknown) that is not a runnable platform and is intentionally excluded.
- Commit ids are taken from the tag names (sha-<40hex>); they were not re-verified against a git checkout in this read-only inventory.
- The Hub API "count" field was observed to be stale; enumeration used the paginated results array (24 tags, next=null) rather than the count field.
- Aliases are derived from tag_last_pushed (tag push/publication time), not a verified image build time; re-pushing a tag can change it.

## Mapping (19 aliases)

| # | Immutable alias | Original sha tag | Manifest digest | Platforms | Publication timestamp |
| ---: | --- | --- | --- | --- | --- |
| 1 | `build-2026-11-14-023420` | `sha-12c22ff93d3749256dc4469da9dd49ec0408a0f8` | `sha256:c875c52ded22be2fb81ee4eb9591c589d25aeb7da788638d2bf15ff566e3b32d` | linux/amd64, linux/arm64 | 2026-11-14T02:34:20Z |
| 2 | `build-2026-11-14-024411` | `sha-4836618be34cb9a933224b9fe0644ee5009be122` | `sha256:e6813b63e6c7efc2fe4c87bea02119fb083a0e24f0c90e4216efbe61d3ffa1c1` | linux/amd64, linux/arm64 | 2026-11-14T02:44:11Z |
| 3 | `build-2026-11-14-025135` | `sha-9b244a0f16d3093116ab93b6111b901cdd7df3d8` | `sha256:b18ff35b8e58739265a7604c5e06a55d26700598c67d9a2ea6617f6f6797936a` | linux/amd64, linux/arm64 | 2026-11-14T02:51:35Z |
| 4 | `build-2026-11-14-030040` | `sha-abb0494191461a5e5aa556fc489dade226b187e5` | `sha256:542de0b6f040bba98956d330b4690fc70fa0286a8d38465d59474c9dd5ad08bd` | linux/amd64, linux/arm64 | 2026-11-14T03:00:40Z |
| 5 | `build-2026-11-14-032449` | `sha-2c5afdc093c1b5f21dab35cef67ba2ba9fac78cd` | `sha256:4c22e227aecf34438356f7ec8addff942f4a2c888e87f84f5d63ecf41820a674` | linux/amd64, linux/arm64 | 2026-11-14T03:24:49Z |
| 6 | `build-2026-11-14-032913` | `sha-66f1f88394d3729b2512fd65985438c52ad81b35` | `sha256:eec5ad30bccc77cee9d0f32641ff9912141e77412cd3b3c8cf1225d1da43413f` | linux/amd64, linux/arm64 | 2026-11-14T03:29:13Z |
| 7 | `build-2026-11-14-034828` | `sha-5b5c914b18c90d2071b827807a583936a0d9c9d4` | `sha256:6d6f736b2e1b841fd4cf6e3d74160012fc04c70569ac8544dc93b3cc324056f5` | linux/amd64, linux/arm64 | 2026-11-14T03:48:28Z |
| 8 | `build-2026-11-17-073548` | `sha-f88748be6a216b187e675bbb1f110df07e544148` | `sha256:ba7345695352aeab9d5cb00943c8034982844e817cff640768b0150fb7c782fd` | linux/amd64, linux/arm64 | 2026-11-17T07:35:48Z |
| 9 | `build-2026-11-17-083252` | `sha-d70585d88c252b6306639c96afb5bbd0bdbbdb79` | `sha256:e01d398396c55e9a240965ef7ab98feb88b1d6de8981c0aa212a486a09f396e4` | linux/amd64, linux/arm64 | 2026-11-17T08:32:52Z |
| 10 | `build-2026-11-17-112844` | `sha-6a2dc9490feba48347479f9aecab240f07f70770` | `sha256:9b897b0b394dc6debc31183ca4a45eb1b97af3c20fdae5f68877dc23d4961cb4` | linux/amd64, linux/arm64 | 2026-11-17T11:28:44Z |
| 11 | `build-2026-11-17-113416` | `sha-ae555ed01b92bcc8279174c8dca8450412daac32` | `sha256:251ff0b72af670d505ae6156069165d7af0ca05f49058c5a14fe619ca56cad3b` | linux/amd64, linux/arm64 | 2026-11-17T11:34:16Z |
| 12 | `build-2026-11-17-133123` | `sha-cbd1e6de16467462343c9b8a87cd4ebb1f9c14df` | `sha256:8df432d95bcacb41a121de593c40c3e1d34eb7638eb411f21d21e5d22876388b` | linux/amd64, linux/arm64 | 2026-11-17T13:31:23Z |
| 13 | `build-2026-11-17-140612` | `sha-80d6a7d775ae4eb59dc653a188003e392849c655` | `sha256:f45a73c2d3839cb0e1e383b92930fdb35c5e201ea4883856220a288b212dcd9e` | linux/amd64, linux/arm64 | 2026-11-17T14:06:12Z |
| 14 | `build-2026-11-17-141147` | `sha-518b64b867d39bffbbe459dbe152b7007baf36cf` | `sha256:bb7042657f1e1f4e9dad6ea2a65d49ccacc4d4df5a5a60b74531529b42bf2430` | linux/amd64, linux/arm64 | 2026-11-17T14:11:47Z |
| 15 | `build-2026-11-17-141910` | `sha-42f478609a65adb3ee8f239d6faa5b211fa6086d` | `sha256:2f66c6414f2bcfe8b8b886fbcecdf6b9a34a6885bb257377f4831f7917397825` | linux/amd64, linux/arm64 | 2026-11-17T14:19:10Z |
| 16 | `build-2026-11-17-142857` | `sha-17044f44de53fcd1f635e0790a048011972bed90` | `sha256:f518aa25ee31ef4737ad89cf17ae40fc792a1c89ea3e5eae775db3de702d2633` | linux/amd64, linux/arm64 | 2026-11-17T14:28:57Z |
| 17 | `build-2026-11-17-233927` | `sha-b897c3cab354e26f54c5922cec9f7112046a9707` | `sha256:54d9b138dfbc39430408ddf02cc57094aeb9bf4c834a118779458fc77957793d` | linux/amd64, linux/arm64 | 2026-11-17T23:39:27Z |
| 18 | `build-2026-11-18-000534` | `sha-c15b8d7d672d8d5fb6459a1317890e2b45adbbb1` | `sha256:4c772558cbcf74b820f2b06485bb0ccab963143de5e82908c19fe59c5a410376` | linux/amd64, linux/arm64 | 2026-11-18T00:05:34Z |
| 19 | `build-2026-11-18-072553` | `sha-d8597d4e953522d2eb2fa6d4764965ae8e58686f` | `sha256:ecc89ba96fb6ea73e3ebb85c4dc8452d8210ce5e96d00fa1c64239c965a9302d` | linux/amd64, linux/arm64 | 2026-11-18T07:25:53Z |

Untouched refs: `latest`, `nightly`, `edge`, `1.0`, `1.0.40`.

## Publication path

`.github/workflows/docker-readable-aliases.yml` runs only on an explicit
trusted-main `workflow_dispatch` with `confirm=true`. It verifies exact main CI
for the pinned commit, validates the checked-in mapping, strictly observes each
source sha ref and target alias, then adds only missing aliases with
`docker buildx imagetools create --tag <alias> <repo>@<digest>`. An existing
matching alias is idempotent; a conflicting digest or an unknown/ambiguous
lookup refuses before any write. Ordinary main merges and dry runs publish
nothing. The mapping is checked in at `docs/docker-readable-aliases.json` and is
recomputable by `scripts/docker_readable_aliases.py map --input <inventory>`.

## Backfill run evidence (placeholder)

Actual backfill is a SEPARATE authorized step after reviewed merge and verified
main CI. It has not been executed. Record here after the run:

- Dispatch commit: _pending_
- CI run URL: _pending_
- Workflow run URL: _pending_
- Aliases created / reused / refused: _pending_
- Post-run digest verification: _pending_

