# Human-readable immutable Docker build aliases

Docker Hub `atharvnaphade/carbonpanel` now has a source-level policy for
readable, immutable build aliases. These are **additions at exact existing
manifest digests**; no image is rebuilt, no layer is pushed, no platform is
changed, and no channel or release alias is moved.

## Naming rule

`build-<YYYYMMDDTHHMMSSZ>-<SHORT12>`

- `YYYYMMDDTHHMMSSZ`: the **observed** Docker Hub tag `last_updated` (UTC),
  truncated to whole seconds.
- `SHORT12`: the first 12 lowercase hex characters of the commit id encoded
  in the original `sha-<commit>` tag name.
- The `build-` prefix and timestamp cannot be mistaken for a semver release
  or a moving `edge`/`nightly` channel.

Collision rules (none were observed in the current inventory):

1. Several `sha-*` tags sharing one manifest digest produce **one** alias;
   canonical is the earliest observed `last_updated` (tie-break: tag name
   ascending). Other sha tags are retained and keep their original refs.
2. Distinct digests mapping to the same base alias append `-<first 8 hex of
   the manifest digest>` to all members; a further collision escalates to the
   full digest hex.

## Historical provenance limits

- last_updated is the Docker Hub tag record last-updated/publication timestamp, NOT a verified image-build time. It can change when tag metadata is edited or when a tag is re-pointed, so it is only a historical provenance proxy.
- Verified: for all 24 observed tags, Hub API last_updated equals tag_last_pushed. The timestamp is therefore the tag push time (not merely a metadata edit), but it is still a publication proxy rather than the original image build time, because a tag can be re-pushed later.
- Observed last_updated for sha-d8597d4e... (07:25:53Z) and the moving "nightly" tag (07:26:45Z) share digest sha256:ecc89ba9...; likewise sha-c15b8d7d... and "edge" share sha256:4c772558.... The close timestamps are consistent with publication time, not necessarily original compile time.
- The Hub images[] list exposes 2 runnable platforms (linux/amd64, linux/arm64). OCI inspection shows an additional attestation manifest (unknown/unknown) that is not a runnable platform and is intentionally excluded.
- Commit ids are taken from the tag names (sha-<40hex>); they were not re-verified against a git checkout in this read-only inventory.
- The Hub API "count" field was observed to be stale; enumeration used the paginated results array (24 tags, next=null) rather than the count field.

The `build-` timestamp is a **tag push / publication proxy**, not a verified
image build time. Historical images are not relabelled as releases or as
nightly builds.

## Current mapping (19 aliases)

| # | Immutable alias | Original sha tag | Manifest digest | Platforms | Observed `last_updated` |
| ---: | --- | --- | --- | --- | --- |
| 1 | `build-20261114T023420Z-12c22ff93d37` | `sha-12c22ff93d3749256dc4469da9dd49ec0408a0f8` | `sha256:c875c52ded22be2fb81ee4eb9591c589d25aeb7da788638d2bf15ff566e3b32d` | linux/amd64, linux/arm64 | 2026-11-14T02:34:20.061022Z |
| 2 | `build-20261114T024411Z-4836618be34c` | `sha-4836618be34cb9a933224b9fe0644ee5009be122` | `sha256:e6813b63e6c7efc2fe4c87bea02119fb083a0e24f0c90e4216efbe61d3ffa1c1` | linux/amd64, linux/arm64 | 2026-11-14T02:44:11.796069Z |
| 3 | `build-20261114T025135Z-9b244a0f16d3` | `sha-9b244a0f16d3093116ab93b6111b901cdd7df3d8` | `sha256:b18ff35b8e58739265a7604c5e06a55d26700598c67d9a2ea6617f6f6797936a` | linux/amd64, linux/arm64 | 2026-11-14T02:51:35.196217Z |
| 4 | `build-20261114T030040Z-abb049419146` | `sha-abb0494191461a5e5aa556fc489dade226b187e5` | `sha256:542de0b6f040bba98956d330b4690fc70fa0286a8d38465d59474c9dd5ad08bd` | linux/amd64, linux/arm64 | 2026-11-14T03:00:40.824918Z |
| 5 | `build-20261114T032449Z-2c5afdc093c1` | `sha-2c5afdc093c1b5f21dab35cef67ba2ba9fac78cd` | `sha256:4c22e227aecf34438356f7ec8addff942f4a2c888e87f84f5d63ecf41820a674` | linux/amd64, linux/arm64 | 2026-11-14T03:24:49.722826Z |
| 6 | `build-20261114T032913Z-66f1f88394d3` | `sha-66f1f88394d3729b2512fd65985438c52ad81b35` | `sha256:eec5ad30bccc77cee9d0f32641ff9912141e77412cd3b3c8cf1225d1da43413f` | linux/amd64, linux/arm64 | 2026-11-14T03:29:13.344639Z |
| 7 | `build-20261114T034828Z-5b5c914b18c9` | `sha-5b5c914b18c90d2071b827807a583936a0d9c9d4` | `sha256:6d6f736b2e1b841fd4cf6e3d74160012fc04c70569ac8544dc93b3cc324056f5` | linux/amd64, linux/arm64 | 2026-11-14T03:48:28.196263Z |
| 8 | `build-20261117T073548Z-f88748be6a21` | `sha-f88748be6a216b187e675bbb1f110df07e544148` | `sha256:ba7345695352aeab9d5cb00943c8034982844e817cff640768b0150fb7c782fd` | linux/amd64, linux/arm64 | 2026-11-17T07:35:48.922126Z |
| 9 | `build-20261117T083252Z-d70585d88c25` | `sha-d70585d88c252b6306639c96afb5bbd0bdbbdb79` | `sha256:e01d398396c55e9a240965ef7ab98feb88b1d6de8981c0aa212a486a09f396e4` | linux/amd64, linux/arm64 | 2026-11-17T08:32:52.1958Z |
| 10 | `build-20261117T112844Z-6a2dc9490feb` | `sha-6a2dc9490feba48347479f9aecab240f07f70770` | `sha256:9b897b0b394dc6debc31183ca4a45eb1b97af3c20fdae5f68877dc23d4961cb4` | linux/amd64, linux/arm64 | 2026-11-17T11:28:44.794878Z |
| 11 | `build-20261117T113416Z-ae555ed01b92` | `sha-ae555ed01b92bcc8279174c8dca8450412daac32` | `sha256:251ff0b72af670d505ae6156069165d7af0ca05f49058c5a14fe619ca56cad3b` | linux/amd64, linux/arm64 | 2026-11-17T11:34:16.48722Z |
| 12 | `build-20261117T133123Z-cbd1e6de1646` | `sha-cbd1e6de16467462343c9b8a87cd4ebb1f9c14df` | `sha256:8df432d95bcacb41a121de593c40c3e1d34eb7638eb411f21d21e5d22876388b` | linux/amd64, linux/arm64 | 2026-11-17T13:31:23.111474Z |
| 13 | `build-20261117T140612Z-80d6a7d775ae` | `sha-80d6a7d775ae4eb59dc653a188003e392849c655` | `sha256:f45a73c2d3839cb0e1e383b92930fdb35c5e201ea4883856220a288b212dcd9e` | linux/amd64, linux/arm64 | 2026-11-17T14:06:12.647238Z |
| 14 | `build-20261117T141147Z-518b64b867d3` | `sha-518b64b867d39bffbbe459dbe152b7007baf36cf` | `sha256:bb7042657f1e1f4e9dad6ea2a65d49ccacc4d4df5a5a60b74531529b42bf2430` | linux/amd64, linux/arm64 | 2026-11-17T14:11:47.375923Z |
| 15 | `build-20261117T141910Z-42f478609a65` | `sha-42f478609a65adb3ee8f239d6faa5b211fa6086d` | `sha256:2f66c6414f2bcfe8b8b886fbcecdf6b9a34a6885bb257377f4831f7917397825` | linux/amd64, linux/arm64 | 2026-11-17T14:19:10.168096Z |
| 16 | `build-20261117T142857Z-17044f44de53` | `sha-17044f44de53fcd1f635e0790a048011972bed90` | `sha256:f518aa25ee31ef4737ad89cf17ae40fc792a1c89ea3e5eae775db3de702d2633` | linux/amd64, linux/arm64 | 2026-11-17T14:28:57.046663Z |
| 17 | `build-20261117T233927Z-b897c3cab354` | `sha-b897c3cab354e26f54c5922cec9f7112046a9707` | `sha256:54d9b138dfbc39430408ddf02cc57094aeb9bf4c834a118779458fc77957793d` | linux/amd64, linux/arm64 | 2026-11-17T23:39:27.693438Z |
| 18 | `build-20261118T000534Z-c15b8d7d672d` | `sha-c15b8d7d672d8d5fb6459a1317890e2b45adbbb1` | `sha256:4c772558cbcf74b820f2b06485bb0ccab963143de5e82908c19fe59c5a410376` | linux/amd64, linux/arm64 | 2026-11-18T00:05:34.147288Z |
| 19 | `build-20261118T072553Z-d8597d4e9535` | `sha-d8597d4e953522d2eb2fa6d4764965ae8e58686f` | `sha256:ecc89ba96fb6ea73e3ebb85c4dc8452d8210ce5e96d00fa1c64239c965a9302d` | linux/amd64, linux/arm64 | 2026-11-18T07:25:53.400494Z |

Original `sha-*` refs remain intact. The following aliases are never moved:
`latest`, `nightly`, `edge`, `1.0`, `1.0.40`.

## Publication path

`.github/workflows/docker-readable-aliases.yml` runs only on an explicit
trusted-main `workflow_dispatch` with `confirm=true`. It verifies exact main
CI for the pinned commit, validates the checked-in mapping, strictly observes
each source sha ref and target alias, then adds only missing aliases with
`docker buildx imagetools create --tag <alias> <repo>@<digest>`. An existing
matching alias is idempotent; a conflicting digest or an unknown/ambiguous
lookup refuses before any write. Ordinary main merges and dry runs publish
nothing. The mapping is checked in at `docs/docker-readable-aliases.json`.

## Backfill run evidence (placeholder)

Actual backfill is a SEPARATE authorized step after reviewed merge and
verified main CI. It has not been executed. Record here after the run:

- Dispatch commit: _pending_
- CI run URL: _pending_
- Workflow run URL: _pending_
- Aliases created / reused / refused: _pending_
- Post-run digest verification: _pending_

