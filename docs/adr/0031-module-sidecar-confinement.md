# ADR 0031: Module sidecar confinement parity

Status: accepted direction; baseline retained, no privilege broadening.

Game containers have a strong baseline: `CapDrop ALL`, only
CHOWN/SETGID/SETUID bootstrap capabilities, `no-new-privileges`, `PidsLimit
512`, read-only root filesystem with a bounded tmpfs, 0-swap memory limits and
loopback RCON publication. Operator overrides are permission-gated and the
effective post-override configuration is checked before creation
(`internal/docker/runtime_matrix_test.go`).

Module sidecars currently enforce only `Privileged=false` and confined bind
sources (`internal/docker/module.go`, `applyModuleConfinement`,
`ValidateModuleVolumeMounts`). Giving them the full game baseline immediately
would break operator host-extension modules that legitimately need additional
capabilities, and there is no explicit per-module exemption path yet.

Decision: do not silently change module confinement in this change. Keep the
existing refusal of `Privileged=true` and docker-socket binds, and treat
"module sidecars inherit the game baseline with a named, permission-gated
exception" as a follow-up that must land with its override contract and
effective-config tests. No qualification claim is made for module isolation
beyond what `internal/docker/module_privilege_test.go` already asserts.
