# ADR 0030: Bounded content provenance

Status: accepted direction; partial implementation.

Content that can be imported, downloaded or executed must have a known origin
and bounded size. The baseline already provides an image repository allowlist
with digest support (`internal/docker/image_ref.go`), verified pinned Geyser and
Floodgate downloads (`cmd/geyser/pin`), checksummed worldstore restores and
bounded generic archive extraction (`pkg/files.ExtractArchiveLimit`).

This change adds two small provenance controls:

- Provider key validation no longer reflects upstream response bodies to the
  console (`internal/indexers/provider_keys.go`). Provider errors return a
  typed status message because an upstream body can echo credentials or
  internal URLs. Soft-result/degraded UI behavior is preserved.
- Datapack installs (indexed download and upload) verify that the destination
  is contained in the server's datapacks directory with `files.WithinReal`
  before writing (`internal/rpc/services/datapack.go`). The filename validator
  already rejects separators; this closes the symlinked-directory case.

Remaining provenance work is deliberately not claimed as done: recording the
resolved image digest per server with an opt-in strict mode, routing native
Packwiz ZIP and other importers through bounded extraction with aggregate caps,
capping multipart reads before `io.ReadAll`, and validating archive download
URLs against the egress policy. Those require coordination with OSS-22/OSS-288
archive primitives and the modpack lanes; see the Track B plan (ADR 0030 scope).
