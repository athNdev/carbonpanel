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
