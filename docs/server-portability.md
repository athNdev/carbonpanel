# Server portability archive format

Carbon Panel's native server archive uses either ZIP or gzip-compressed TAR. Both
encodings carry the same versioned manifest and verified server files. The
archive represents one server's managed data directory. For an operator to
recreate a server on a different installation, its destination node, network
settings and credentials must be supplied there.

## Schema 1

- The root entry `manifest.json` is UTF-8 JSON. It records `schema_version`,
  `exporter_version`, `runtime` and `files`. The writer emits it after the data.
- `runtime` records `minecraft_version`, `mod_loader`, optional
  `loader_version`, `memory_mb` and `max_players`. The optional
  `requires_custom_runtime` and `requires_provider_setup` flags signal
  dependencies that require destination configuration.
- Each record in `files` gives a slash-separated relative `path`, the exact
  uncompressed `size` in bytes, and the lowercase hexadecimal `sha256`.
  Its corresponding archive member is `data/<path>`. Every member must
  match one record. Checksums detect corruption. The operator supplies trust
  in the source because the manifest carries no signature.
- Paths must be portable across Linux and Windows. The format includes regular
  files and uses implicit directories. It rejects duplicate or conflicting
  paths, special files and links. Archives allow at most 50,000 files and
  32 GiB of expanded data. A deployment may configure a lower upload limit.
- ZIP archives have no archive comment. TAR members may use PAX path and size
  extensions. The TAR is contained in one gzip stream.

## Included data

World files, mods, plugins and game configuration under the managed data
directory are portable. Each captured file retains its bytes. The export
copies recognized gameplay settings from the root `server.properties` and
omits credentials, endpoint settings and comments from that file. It also
removes the Velocity forwarding configuration from Paper configuration.

The archive omits panel state, Docker overrides, node or host paths, panel and
RCON credentials, recognized key and credential files, logs, crash reports,
session locks and storage transaction files. The panel treats third-party
plugin and mod data as opaque and cannot identify secrets placed inside those
files. Review such data before sharing an archive. Supply destination-specific
plugin connections and credentials separately.

The consistency policy requires a stopped server. If third-party software can
write to the data directory independently, stop those writers before exporting.

## Export and create from an archive

Use the stopped-server consistency option. Export verifies the game container
and attached module containers on their owning nodes before reading the data.
The download session has the same server-scoped file authorization as other
binary downloads.

Upload the archive through the standard chunked upload service. Preview checks
every file checksum, reports runtime compatibility and gives the expanded
size and the destination's available space after node selection. Its space
requirement includes an allowance for filesystem allocation and first-boot
writes. A preview does not create a server. Export preparation, preview, and
archive creation can take longer than ordinary metadata calls. These calls
have a one-hour controller deadline and respect earlier client cancellation.

Preview is authorized as server creation, not node inventory. The inspection
response therefore also returns a narrow list of enabled destination IDs and
display names, while the full node list continues to require nodes/read. A
caller who can create servers but cannot read nodes can select one of those
destinations, preview it again for the exact-node storage check and submit the
same node. The list carries no addresses, credentials or capacity statistics.

Provide a new name, a destination node and a free port when creating the
server. Keep the same request ID for retries of the same submission. The
controller validates the caller's upload and the destination again. It stages
the data into a fresh directory on that node, then commits the new server and
initial config together. If the copy is interrupted, it removes the directory
allocated by that attempt. Once the row is committed, the verified data is
retained for recovery if container creation or boot fails.

The initial import path supports the managed runtime and the Minecraft and
loader versions recorded in the archive. Preview reports a blocker when the
source depended on custom container settings, managed provider setup or
attached modules that the archive cannot reproduce. A transport-only node
requires a storage worker that supports exclusive directory allocation and
free-space queries.

## Operator controls

The server detail page offers export as ZIP or gzip-compressed TAR with
stopped-server consistency only. It shows a preparation state while the
controller validates and compresses the data, then returns a download session
through the same server-scoped authorized download transport used by other
binary downloads. Before enabling the download, the console shows this notice:
"Third-party plugin and mod files are included and can contain credentials.
Review the archive before sharing it." The exporter filters only recognized
managed settings and paths; it does not inspect the contents of third-party
plugin or mod files for secrets. Export never stops a live server: an operator
must stop it first, and the controller refuses a running server.

The create-server wizard offers a Create-from-Archive path. It uploads the
archive through the standard chunked upload service and previews it after
upload, including the chosen destination node's capacity once a node is
available. The preview shows the archive format, schema and exporter version,
runtime defaults, file count, expanded and required bytes, destination
availability, the chosen port and typed blockers or warnings. Archive mode
requires a concrete destination node so the preview can check that node's free
space; automatic placement remains available for ordinary creates. A create-only
operator whose role denies node inventory sees the enabled destination IDs and
display names from the preview instead of the full node list. Node inventory,
when readable, only seeds a preliminary choice: a successful preflight for the
current upload is authoritative, so nodes enabled since the page loaded appear
and nodes disabled since then disappear, while a still-eligible selection is
kept. A settled preflight with no enabled destinations shows an actionable empty
state with a preflight retry instead of waiting indefinitely. Create stays
disabled until the current upload has a successful preview for that exact node,
and the create request submits precisely the previewed node. The final step names the
remaining blocker when create is unavailable. A replacement upload blocks
submission while it is in flight, and a late result for a superseded upload or
destination is ignored. The visible engine and loader identity follows the
verified archive runtime, while operator memory and max-player overrides remain
editable. Docker image, Detached Mode and Docker overrides are hidden in
archive mode, and the request clears any stale values from an earlier mode.
Leaving archive mode restores the ordinary auto/manual placement choice.
Standalone setup add-ons are disabled for an archive import because the archive
already carries its mods, plugins, datapacks and configuration; manage them from
the server page after creation. A failed preview offers a same-node retry. The
controller revalidates the upload and destination on create. A create
submission keeps one request ID across retries; a refusal retains the completed
upload so the operator can correct the destination and retry. While that create
is in flight, step, cancel and browser-back navigation are blocked and the
submission completion is bound to the upload, node and preview captured when it
started. A successful create discards the submission identity. Neither path
stops an existing server.

Export preparation, preview and create-from-archive validation use an extended
one-hour client deadline and respect earlier cancellation. The console reports
a preparation state rather than presenting these as ordinary metadata calls.
