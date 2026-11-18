# ADR 0028: Credential generation, detection and rotation

Status: accepted; backup-key default change deliberately deferred.

New servers receive a CSPRNG RCON password (`internal/db/rcon_password.go`,
`GenerateRCONPassword`). The former deterministic `CARBONPANEL_default` /
`CARBONPANEL_<serverID>` scheme is retained only as a *detection* marker via
`IsWeakRCONPassword`; persisted values are never silently rewritten. At startup
the controller warns for each server whose RCON is enabled but whose password
is weak or blank.

Blank RCON passwords are a migration case, not an error: upstream Minecraft
treats a blank `rcon.password` as "RCON disabled". The panel therefore warns and
keeps booting; changing it would silently enable a management interface and
break operators who intentionally disabled RCON. Rotation uses the existing
server-configuration RPC/UI path; no world or container mutation occurs.

Role grants were also tightened: a custom role holding `roles:create` or
`roles:update` can no longer assign the built-in `admin` role or grant a
wildcard resource/action. Those operations require the non-default
`roles/manage_wildcard` capability, which only the built-in admin role has by
default. Anonymous RBAC policies are still seeded for both settings; the auth
layer remains the gate that refuses to mint an anonymous identity when access
is disabled. Changing the seeding would alter the permission-matrix UI and was
not part of this change.

Backup-encryption custody gating: enabling encryption by default requires a
documented key location outside `data_dir`/`backup_dir`, a recovery path with
key ID/fingerprint and restore drill, a rotation path that keeps old keys while
any archive references them, and explicit operator acknowledgement. Until all
four are verified, `backupcrypt.ResolveKey` stays opt-in (empty key = plaintext)
and existing archives are never re-encrypted silently. An unrecoverable-backup
risk blocks the default change.

Recovery-key auditing **is implemented here**, directly against the immutable
activity log in `AuthService.UseRecoveryKey`: successful use emits
`auth.recovery_used` and an invalid key emits `auth.denied`, with source IP but
never the key value. This is an event write, **not** a new cross-track hook; no
B-local `BootstrapAudit`/`SessionValidator` interface was created. The orchestrator
should relay this concrete event contract to Track A so any later consolidation
uses these names instead of inventing a parallel one.

Still owned by Track A through the B-00 contracts and not implemented here:
first-admin claim atomicity across the local and OIDC paths, and API-token
revocation on password change.
