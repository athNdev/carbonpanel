# Knowledge MCP capability matrix (OSS-24)

This is the authoritative record of what the repository can *prove* about the
pinned MCP servers used by the git-native knowledge base. It deliberately
separates two different claims:

1. **Server-side stdio capability** — verifiable by a deterministic, owned
   fixture in this repository.
2. **End-to-end use inside the Zed editor** — a GUI claim that this repository
   cannot script or prove.

Do not treat (1) as evidence for (2).

## Pinned configuration

Both configs pin the same servers; only the top-level key differs.

| Client config | Servers |
| --- | --- |
| `.mcp.json` (`mcpServers`) | `carbon-knowledge`: `@modelcontextprotocol/server-filesystem@2.0.0` with `knowledge`; `carbon-git`: `@cyanheads/git-mcp-server@2.15.3` with `GIT_BASE_DIR=.`, `MCP_TRANSPORT_TYPE=stdio` |
| `.zed/settings.json` (`context_servers`) | the same two commands, arguments and environment |

The CLI equivalents are `make knowledge-mcp` and `make knowledge-mcp-git`.

## Reproducing the proof

```sh
node scripts/mcp-smoke.mjs     # or: make knowledge-mcp-smoke
```

The harness spawns the two pinned servers over real stdio (newline-delimited
JSON-RPC, protocol `2025-11-25`), sends `initialize`,
`notifications/initialized`, `tools/list` and `tools/call`, and removes every
temporary directory it creates. It never reads or writes the repository, a
user configuration, or a Zed profile. The knowledge CI job also runs it.

`GIT_SIGN_COMMITS=false` and local `commit.gpgsign=false` are set in the
disposable fixture so a host signing policy cannot make the proof depend on a
private key.

## Exact `tools/call` evidence

Every frame below has the shape
`{"jsonrpc":"2.0","id":N,"method":"tools/call","params":{"name":...,"arguments":...}}`.
Tool arguments whose property names vary between server versions are
discovered from `tools/list` `inputSchema.properties` rather than guessed.

| Verb | Pinned tool | Exact arguments used by the harness | Assertion |
| --- | --- | --- | --- |
| Read | `read_text_file` | `{"path":"<temp>/smoke.md"}` | returned text contains the bytes just written or edited |
| Write | `write_file` | `{"path":"<temp>/smoke.md","content":"# Smoke\n\nMCP works.\n"}` | read-back contains `MCP works.` |
| Edit | `edit_file` | `{"path":"<temp>/smoke.md","edits":[{"oldText":"MCP works.","newText":"MCP edits."}]}` | read-back contains `MCP edits.` |
| Commit | `git_commit` | `{"path":"<temp repo>","message":"smoke commit"}` | message appears in the commit result and in `git_log` |
| Merge | `git_merge` | `{"path":"<temp repo>","branch":"smoke-feature","noFastForward":true,"message":"smoke merge"}` | feature commit appears in the merged log |

Merge coverage includes three cases, all inside the temp repository:

- **Additive merge** — a feature branch adds a file; `git_merge` integrates it.
- **Conflict detection** — two branches change the same line; `git_merge`
  reports a conflict (tool error or conflict text).
- **Conflict resolution** — a second filesystem MCP server scoped to the temp
  repo writes the resolved file with `write_file`; `git_add` and `git_commit`
  record `resolve smoke conflict`, asserted in the result.

The harness also asserts `tools/list` advertises `read_text_file`,
`write_file`, `edit_file`, `search_files`, `git_status`, `git_add`,
`git_commit`, `git_log` and `git_merge`. A missing tool fails the run.

## What is proven, and what is not

| Claim | Status |
| --- | --- |
| Filesystem MCP read/write/edit/search over real stdio | **Proven** by the owned fixture, with content assertions |
| Git MCP status/add over real stdio | Called and required to succeed; no independent result assertion |
| Git MCP commit over real stdio | **Proven**, message asserted in commit and log |
| Git MCP merge and conflict resolution over real stdio | **Proven** by the owned fixture, including a resolved conflict commit |
| The pinned `carbon-git` catalog advertises `git_merge` | **Proven** (`tools/list`), 28 tools total |
| These tools work **inside Zed** via `.zed/settings.json` | **Not proven by this repository.** External, interactive verification is required |

## Exact remaining Zed blocker

A real "MCP read, write, commit and merge work in Zed" end-to-end run cannot be
produced through a scripted safe owned fixture on this machine.

Zed 1.29.1 is installed in the user's local application directory
(`%LOCALAPPDATA%\Programs\Zed`), with a separate Preview installation. Its CLI
shim is only a GUI launcher: `zed --help` exposes
`--wait`, `--add`, `--new`, `--existing`, `--user-data-dir`, `--foreground`,
`--zed`, `--dev-server-token`, `--wsl`, `--dev-container`, `--diff`,
`--completions`, `--version` and `--help`. There is **no** `--mcp`,
`--agent`, `--assistant`, `--tool` or headless mode. `--user-data-dir <DIR>`
can isolate Zed's user data from real configuration, but it cannot make Zed
load `.zed/settings.json` context servers or issue `tools/list` / `tools/call`.
That requires the interactive in-editor assistant, first-use trust/enablement
of project context servers, and likely a signed-in Zed account. The Zed
installation is also a real user installation, not an owned fixture, so using
it from an automated run would risk touching user configuration.

**Remaining external step (human-owned):** open this repository in Zed with the
project `.zed/settings.json` trusted, then use the assistant to read and edit a
knowledge concept, commit it, and merge a conflict-resolution branch. Record
the observed tool calls. Until that record exists, the "in Zed" clause of
OSS-24 is unproven and must not be marked complete.

The CLI fallback remains authoritative: MCP is an accelerator and is never
required to build, test or merge a change.
