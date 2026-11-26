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

## Restored gameplay settings

The managed image writes its environment back into `server.properties` on
startup. To keep archived gameplay from being replaced by panel defaults, the
controller parses a typed allowlist from the already filtered, checksum-validated
root `server.properties`: `level-name`, `difficulty`, `gamemode`,
`sync-chunk-writes` and `pause-when-empty-seconds`. It validates each value and
writes the accepted values into the new server's initial config in the same
transaction that creates the server row, before the container is created.

Precedence is fixed:

1. Destination RCON, ports, routing, proxy settings, authentication, image and
   ownership always come from the destination. The archive cannot carry them.
2. A present and valid archived gameplay value wins over the panel default for
   that property. This includes the panel's forced portable defaults for
   `sync-chunk-writes` and `pause-when-empty-seconds`.
3. An absent, empty or unsupported value keeps the panel default. The import is
   not refused for it, and the panel never persists a value the game would
   reject.
4. Other recognized properties keep their archived bytes. A destination global
   setting that forces an environment value outside this list still wins at
   startup.

The consistency policy requires a stopped server. Export holds the server
lifecycle transition and the server's module lifecycle gate for the whole read.
The gate serializes module create, start, stop, update, recreate and delete
paths, including their asynchronous workers; an export refuses while one of
those operations is in flight. Module create and update reserve that gate before
acknowledging the request, so a successful response cannot leave a worker that
races a later export. Direct panel file writes hold a shared data-write claim
and are refused while an export owns the server. Export verifies the game
container and attached module containers on their owning nodes. It refuses an
attached module whose resolved bind mounts can write the server data directory,
whose recorded container is missing, or whose live effective data-directory
mounts differ from the stored configuration. A writable mount whose source is
an ancestor of the data directory is treated as an overlap just like a nested
mount. The stored-versus-live comparison catches an asynchronous recreate that
failed after the database changed. Make the module's data-directory bind mounts
read-only and recreate it, or remove the module, before exporting.

A stopped container does not by itself qualify writer process death. The policy
covers panel-driven lifecycle operations only; it does not prove that an
independent host process, an external daemon, or a container restart policy
cannot still write. Stop such writers before exporting.

## Export and create from an archive

Use the stopped-server consistency option. Export verifies the game container
and attached module containers on their owning nodes before reading the data,
and refuses attached modules whose stored or live mounts can write the
data-directory or whose live mounts do not match the stored configuration. The
download session has the same server-scoped file authorization as other binary
downloads.

Upload the archive through the standard chunked upload service. Preview checks
every file checksum, reports runtime compatibility and gives the expanded
size and the destination's available space after node selection. Its space
requirement includes an allowance for filesystem allocation and first-boot
writes. A preview does not create a server. Export preparation, preview, and
archive creation can take longer than ordinary metadata calls. These calls
have a one-hour controller deadline and respect earlier client cancellation.

Provide a new name, a destination node and a free port when creating the
server. Keep the same request ID for retries of the same submission; the
durable identity records the verified archive content and manifest, so a fresh
upload id is accepted after a controller restart only when the independently
validated content is identical. The controller validates the caller's upload
and the destination again. It records the chosen destination and archive
identity durably in the same transaction as the request claim, resolves and
records the exact allocation root, allocates a fresh directory on that node,
writes an ownership marker and then stages the data. It commits the new server,
initial config and the intent's committed phase together. If the copy is
interrupted, it removes only the directory whose marker and recorded allocation
root prove that attempt created it. Once the row is committed, the verified
data is retained for recovery if container creation or boot fails.

After a crash between the request claim and the row commit, a retry reads the
durable intent. It validates the supplied upload against the recorded content
identity before touching any destination. A destination that exists and
matches the recorded node, allocation root, path and marker is reclaimed and
the import resumes. A missing server row and a missing destination lets the
import start again. Recovery never reclaims a destination whose intent phase is
committed, including after a normal keep-data delete whose marker cleanup was
interrupted; published data belongs to the operator. If the allocation root
changed, or the marker is missing or does not match, the retry fails with a
visible destination-ownership error and leaves the directory untouched. Inspect
the node's server storage before retrying, or use a new request id.

The initial import path supports the managed runtime and the Minecraft and
loader versions recorded in the archive. Preview reports a blocker when the
source depended on custom container settings, managed provider setup or
attached modules that the archive cannot reproduce. A transport-only node
requires a storage worker that supports exclusive directory allocation and
free-space queries.
