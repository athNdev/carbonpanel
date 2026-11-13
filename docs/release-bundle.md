# Carbon Panel release bundle

This archive contains Carbon Panel with its web interface embedded, the
`carbon-storage` helper, an example configuration, and license notices.
`BUILD-INFO.json` identifies the version, source commit, build time and platform.
Run `carbon-panel --version` to inspect the binary without starting the service.

1. Install Docker and make a Docker daemon available to Carbon Panel. Windows
   and macOS hosts need a daemon running Linux containers.
2. Extract the archive into a new directory. Copy `config.example.yaml` to
   `config.yaml` and configure authentication, storage and Docker access.
3. Run `./carbon-panel --config ./config.yaml` (on Windows,
   `.\carbon-panel.exe --config .\config.yaml`).

Before upgrading, stop the panel and back up its database and configuration.
Keep existing world directories, backups and credentials. Replace program files
only; the example configuration is a reference, not an upgrade migration.

These are portable command-line archives, not signed installers. Minecraft,
Docker and Java runtimes are not bundled. The storage helper in this archive
matches the controller platform; remote Linux storage workers are supplied by
the controller's matching Linux container image.

Documentation: https://carbonpanel.dev/docs/
Release and checksum instructions: https://github.com/athNdev/carbonpanel/blob/main/docs/releases.md
