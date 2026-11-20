# World Library import archive layout

World Library revisions are immutable ZIP archives. The binder publishes whole
top-level **level families** into a server's level folder, so the archive layout
must match what binding can safely map. One bounded, portable family rule is
shared by import, compatibility preview and bind; all three refuse the same
layouts before any live-world change.

## Supported layout

- A usable family is a top-level folder with a direct `level.dat` marker, for
  example `world/level.dat`. Layout checks, compatibility findings, and
  extraction use the same normalized forward-slash paths. One or more
  usable families are supported:
  - a single Java world (`world/level.dat` with `region/`, `datapacks/` and
    dimension data under `world/`);
  - Paper/Spigot/Bukkit split dimensions with their own markers:
    `world/level.dat`, `world_nether/level.dat`, `world_the_end/level.dat`;
  - vanilla/Forge/Fabric `DIM-1` and `DIM1` data **inside** the `world` family
    (those subdirectories do not need their own `level.dat`).
- Non-`level.dat` companions are allowed and preserved in the stored revision:
  `server.properties`, `config/`, mods/plugins inside a family, and similar
  backup metadata. The binder publishes usable level families; root-level
  companions are not promoted into the live world.
- The selected target level must be one of the usable families. Only that
  level and its recognised dimension siblings (`<level>_nether`,
  `<level>_the_end`, `<level>_end`) are published; unrelated named worlds in
  the same archive are refused rather than replaced. Preview and bind both use
  the server's configured level (with a differing bind request refused). Binder
  re-reads that level under admission before journaling or moving data, and
  passes the canonical value to storage and the audit event. Version-only
  previews (no server) still apply the layout policy without a configured level.
- While a world bind owns the server's operation journal, global settings sync
  preserves the configured level and the custom properties that can override
  it. Other global settings can still be synchronized. The sync total counts
  saved changes and restored defaults.

## Refused layout (actionable, pre-mutation)

- `level.dat` at the archive root (flat archive): repack the world folder itself.
- Mixed root `level.dat` plus named families (ambiguous), or no usable family
  (including nested `server/world/level.dat`, hidden `.world/level.dat`, case
  variants such as `WORLD/LEVEL.DAT`, backslash separators such as
  `world\level.dat`, and zero-byte markers): repack into a named top-level
  folder with an exact lowercase, non-empty `level.dat` using forward slashes.
  Backslashes in archive paths are refused because node filesystems interpret
  them differently.
- A family name that does not match the selected target level (for example
  `MySave/level.dat` selected for `world`): rename the folder or choose the
  matching target. The available families are named in the error.

Refusals create no revision or entry at import, and at preview/bind they do not
stop or restart the server, create a binding row, journal row or activity event,
or write live-world data. Immutable revision bytes are never modified. The
preflight uses the existing bounded archive limits and honours cancellation.

## Revisions imported before this gate

Flat, ambiguous or mismatched revisions already stored in the library are
checked again by read-only preview (`CheckCompatibility`) and by `BindWorld`.
Both refuse them with the same actionable message. Supported named-family
revisions and backups keep working unchanged.
