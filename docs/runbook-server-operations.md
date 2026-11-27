# Server lifecycle operations

The panel records a journal row before it creates, starts, stops, restarts,
deletes, or migrates a server container. The row holds a per-server lock until
the mutation finishes or startup has observed the container.

A repeat of the same client `request_id` for the same server and action does
not run that mutation twice. A new attempt after a failure uses a new
`request_id`. The same id is stored on the matching activity-feed row so the
journal and the audit trail join.

Open the server’s **Activity** tab to see both sources. **Lifecycle operations**
shows operation outcomes and recovery steps. **Audit log** shows recorded
actions, actors and any explicit outcomes. Expand **Details** to see the
request ID when present. An entry labeled **Recorded** carries no explicit
result. The log includes server-scoped player activity written after the
server ID was added to those audit records. Older player audit entries with
no server ID remain in the global audit API.

Each lifecycle row also shows its node, endpoint generation, request key and
durable phase, so it can be joined to a server or node. Where daemon identity
matters the row shows a derived correlation state: **matched**, **unmatched**
or **unknown**. A raw Docker daemon id, host or socket is never shown. An
**unknown** correlation means the panel could not compare daemon identity, not
that the operation failed.

An **unknown** outcome with reason code `executing_stale` or
`uncertain_external_effect` may already have changed the world. Do not retry it
blindly. Check the container and world on the node first, then use **Release
lock** (or `-operations resolve`) if the lock still needs releasing.

## When a server stays locked

If the panel restarts while a mutation is in progress, startup reads the
container from Docker and releases the lock when that read succeeds. If the
daemon cannot be reached, the lock stays and the panel retries the read until
the daemon answers. Nothing is deleted while the outcome is unknown.

## List and release a lock

Both commands open the panel database and exit. They do not start the API and
they do not change containers.

```sh
carbon-panel -config config.yaml -operations list
carbon-panel -config config.yaml -operations resolve -operation-id <id> -operation-note "container was already stopped"
```

`list` prints the node, generation, request key and derived correlation state.
It never prints the Docker daemon id.

`resolve` is the operator escape hatch when the daemon will not return and the
container has been checked by hand. It records the note, marks the row
recovered, and clears the lock. It does not stop, remove, or prune containers.

## Release a port left by detach

Detaching a server removes its panel row and leaves the container, the world,
and the host port lease. After the container is stopped by hand, release the
lease so another server can use the port:

```sh
carbon-panel -config config.yaml -operations ports
carbon-panel -config config.yaml -operations release-port -server-id <server-id>
```

`release-port` refuses while that server row still exists. It does not stop
the container or delete the world.

Finished rows are deleted after 30 days. A row that still holds a lock is kept.
