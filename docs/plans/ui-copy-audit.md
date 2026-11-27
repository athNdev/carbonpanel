# UI Copy Audit — Web Console (OSS-465)

Branch: `feat/v2.1.1-improvements` · Scope: `web/carbon-panel/src` (SvelteKit / Svelte 5, IBM Carbon voice)
Jira: OSS-465 (child of OSS-448). Status of this document: audit complete; replacements not yet applied.

> **PR #729 split note (2026-11-27):** The per-row "Stage this update"
> control, the staged-outcome labels and `web/carbon-panel/src/lib/mod-update-ui.ts`
> were removed by the PR #729 split, so the mods entries that reference them
> below (#39 and the pinned `server-mods-update.test.ts` list) are historical
> and are not copy to preserve. The retained surface is the read-only #680
> installed-mod preview in `internal/rpc/services/mod_update_preview.go`,
> summarized by `summarizeModUpdateCheck()` in
> `web/carbon-panel/src/lib/utils/mod-update-summary.ts`, and rendered by the
> server-mods preview row with no stage or apply controls.

## 1. Method

Systematic sweep of every `.svelte` route page and component plus the user-facing label
maps in `lib/features/**` and `lib/utils/**` and `lib/*.ts` helpers. Evidence gathered via
pattern greps (`placeholder=`, `aria-label=`, `title=`, `description=`, `helperText=`,
`toast.*`, empty/error states, confirm dialogs, button text nodes) and full reads of the
primary flows (dashboard, servers list/detail/create wizard, login, modpacks, studio,
players, world library, console). Byte-level verification was used where an encoding bug
was suspected. Strings pinned by `*.test.ts` contract tests are flagged in §6.

Voice target (already mostly followed by newer surfaces): **terse, technical,
operator-oriented**. Sentence case for labels/buttons; no exclamation marks; no "Please";
no "your"/"you" where the operator is implied; no marketing filler; active voice;
"the panel" may stand in for Carbon Panel but must be used consistently.

Total distinct findings recorded: **92** (P0: 2 · P1: 24 · P2: 41 · P3: 25, see §6).

---

## 2. Findings

Categories: **(a)** UNNECESSARY — states what the UI already shows / filler / redundant
instruction · **(b)** CONVERSATIONAL — exclamation, chattiness, hedging ·
**(c)** AWKWARD — grammar, over-long, wrong voice, confusing phrasing ·
**(d)** INCONSISTENT — terminology or casing differs from the canonical term (§3).

### 2.1 Encoding / rendering defects (fix before anything else)

| File:line | Current | Proposed | Category |
|---|---|---|---|
| `routes/login/+page.svelte:327` | `Checking sign-in options` followed by **U+FFFD REPLACEMENT CHARACTER** (bytes `EF BF BD`, verified) | `Checking sign-in options…` (U+2026) | (c) corrupted character |

### 2.2 UNNECESSARY (a)

| File:line | Current | Proposed | Note |
|---|---|---|---|
| `routes/login/+page.svelte:408` | placeholder `Enter your username` | `username` | instruction repeats the label |
| `routes/login/+page.svelte:418` | placeholder `Enter your password` | `••••••••` or blank | same |
| `routes/login/+page.svelte:362` / `506` | placeholder `Confirm your password` | `Repeat password` | instruction |
| `routes/login/+page.svelte:517` | placeholder `Enter invite PIN` | `PIN` | instruction |
| `routes/login/+page.svelte:352` / `497` | placeholder `Choose a strong password (min 12 chars)` / `Choose a password (min 12 chars)` | `Minimum 12 characters` (matches profile/`CarbonShell` pattern) | wordy; two variants for one rule |
| `routes/profile/+page.svelte:421` | placeholder `Confirm your new password` | `Repeat new password` | instruction |
| `routes/servers/+page.svelte:356-358` | `Create your first Minecraft server instance to begin managing containers and game topology.` | `Create your first Minecraft server.` | filler tail; "game topology" is undefined jargon |
| `routes/+page.svelte:633-634` | `Create your first Minecraft server instance` | `Create your first Minecraft server` | same, dashboard empty state |
| `routes/modpacks/+page.svelte:789` | `Click the heart icon on any package to add it to your pinned favorites.` | `Use the heart icon to pin a modpack to favorites.` | "Click" + "your" |
| `routes/modpacks/+page.svelte:791-792` | `Click "Sync" to index popular modpacks, or use "Import Modpack" to add a ZIP archive or a direct URL.` | `Sync indexes the Modrinth/CurseForge catalogs. Import adds a ZIP or URL.` | "Click" quoting UI text |
| `routes/profile/+page.svelte:619-620` | `This bearer token will never be displayed again. Store it securely in your secrets manager or CI/CD environment.` | `This token is shown once. Store it in a secrets manager or CI secret.` | "your", wordy |
| `routes/docs/api/+page.svelte:226` | `Live ConnectRPC + OpenAPI reference. Spec served from /api/v1/openapi.yaml (regenerated via make gen)` | `Generated from the service OpenAPI spec.` | internal build command (`make gen`) in user copy |
| `routes/settings/+page.svelte:187` | `This section needs permissions that are not loaded, or the page data failed to load. Retry, or contact an administrator if you lack access.` | `This section needs permissions that are not loaded, or its data failed to load. Retry, or contact an administrator.` | trailing clause repeats the first |
| `routes/login/+page.svelte:372-374` | `A local admin account is required for initial setup, even with SSO enabled. This ensures you always have a fallback login to manage the system if your identity provider becomes unavailable.` | `A local admin account is required for initial setup, even with SSO enabled, so the panel stays manageable if the identity provider is unavailable.` | 2 sentences → 1, removes "you" |
| `routes/modpacks/studio/[id]/+page.svelte:992` | `Adjust the side filter or search, or browse the catalog to add mods.` | `Adjust the side filter or search the catalog.` | doubled "or" |
| `lib/components/support-settings.svelte:138` | `Click the download button to save the bundle.` | `Download the bundle to save it.` | "Click" |
| `lib/components/node-settings.svelte:886` | `Auto-detect found reachable Docker daemons on this host's network. Register the ones you want the placement engine to use — you can adjust names, IPs, and limits later.` | `Reachable Docker daemons found on this host's network. Register the ones the placement engine should use; names, IPs, and limits can be changed later.` | wordy, "you" |
| `routes/servers/new/+page.svelte:1808-1809` | `Cluster scheduler selects optimal worker node based on available memory and active workload.` | `The cluster scheduler picks the node with the most free memory and fewest servers.` | missing articles; "workload" vague |
| `routes/servers/new/+page.svelte:1807-1808` (manual tile) | `Pin this Minecraft container instance to a designated Docker host machine.` | `Pin this server to a specific Docker node.` | "Minecraft container instance"/"host machine" padding |
| `routes/modpacks/+page.svelte:850` | modal description `Add a modpack package to your library from a ZIP archive or a direct URL` | `Add a modpack from a ZIP archive or direct URL` | "package to your library" filler |

### 2.3 CONVERSATIONAL (b)

| File:line | Current | Proposed | Note |
|---|---|---|---|
| `routes/login/+page.svelte:305` | `Welcome! Create your initial administrator account.` | `Create the initial administrator account.` | exclamation, "your" |
| `routes/login/+page.svelte:592` | `Forgot access? Recovery` | `Lost access? Use a recovery key` | cryptic fragment |
| `routes/profile/+page.svelte:573` | `Token Created Successfully` | `Token created` | Title Case + "Successfully" |
| `routes/profile/+page.svelte:642` | `Copied!` | `Copied` | exclamation |
| `routes/profile/+page.svelte:617` | `Save this secret now` | `Save this token now` | minor; "secret" vs "token" mixed (see §3) |
| `routes/modpacks/+page.svelte:386-388` | `No active Docker nodes available — add and enable a node in Settings → Docker Nodes before creating a server.` | `No active Docker nodes. Register one in Settings → Docker Nodes first.` | aligns with wizard phrasing (see §2.5 #54) |
| `routes/modpacks/studio/+page.svelte:239` | `Successfully imported "${imported.name}"!` | `Imported "${imported.name}"` | exclamation + "Successfully" |
| `routes/modpacks/studio/[id]/+page.svelte:455` | `Packwiz re-indexed & refreshed successfully` | `Packwiz index refreshed` | "Successfully", "&" |
| `routes/modpacks/studio/[id]/+page.svelte:520` | `Added ${item.title} (${side.toUpperCase()}) to pack!` | `Added ${item.title} (${side}) to the pack` | exclamation |
| `routes/modpacks/studio/[id]/+page.svelte:1401` | `Copied PACKWIZ_URL to clipboard!` | `Copied PACKWIZ_URL` | exclamation |
| `routes/modpacks/studio/[id]/+page.svelte:311` | `Updated ${mod.name} side to ${newSide.toUpperCase()}` | `Set ${mod.name} side to ${newSide}` | ALL-CAPS side value in prose |
| `lib/components/modpack-deploy-dialog.svelte:100-101` | `Deployed successfully!` | `Modpack deployed` | exclamation |
| `lib/components/server-settings.svelte:301` | `Server migrated to ${target} successfully!` | `Server migrated to ${target}` | exclamation |
| `lib/components/support-settings.svelte:119` | `Support bundle uploaded successfully!` | `Support bundle uploaded` | exclamation |
| `lib/components/support-settings.svelte:137` | `Support bundle generated!` | `Support bundle generated` | exclamation |
| `lib/components/support-settings.svelte:175` | `Support bundle downloaded!` | `Support bundle downloaded` | exclamation |
| `lib/components/support-settings.svelte:188` | `Reference ID copied to clipboard!` | `Reference ID copied` | exclamation |
| `routes/+error.svelte:22` | `Something went wrong` (non-404 title) | `Page failed to load` | vague/conversational; body line 25 already carries the detail |
| `routes/modpacks/+page.svelte:601` | `Sync {indexerName}` | `Sync from {indexerName}` | reads as "sync the string" |
| `routes/login/+page.svelte:611` | `Continue as Guest` | `Continue as guest` | capitalization |

Broad class (P3, 12 instances, same pattern): `X successfully` toasts → `X`.
`routes/settings/+page.svelte:102`, `routes/login/+page.svelte:172,207,208`,
`routes/profile/+page.svelte:219`, `lib/components/api-keys-settings.svelte:122`,
`routes/modpacks/+page.svelte:318,363,413`, `lib/components/node-settings.svelte:317,412`,
`lib/components/user-settings.svelte:182,208,232`, `lib/components/role-settings.svelte:182,210`,
`lib/components/server-tasks.svelte:645,665,694,715`,
`lib/components/files/file-editor-dialog.svelte:74`, `lib/components/CarbonShell.svelte:561`.
Proposed pattern: `Password changed`, `Role created`, `Task deleted`, `File saved`, …

Broad class (P2, 14 instances): drop "Please" from validation toasts.
`routes/servers/new/+page.svelte:655,944`, `routes/modpacks/+page.svelte:292,348`,
`routes/modpacks/studio/+page.svelte:179,213`, `routes/modpacks/studio/[id]/+page.svelte:386`,
`routes/profile/+page.svelte:202`, `lib/components/server-configuration.svelte:380`,
`lib/components/manifest-inspector-dialog.svelte:47`, `lib/components/modpack-deploy-dialog.svelte:79`,
`lib/components/node-settings.svelte` (implicit), `lib/components/api-keys-settings.svelte:62`.
Proposed pattern: state the requirement — `Enter a valid, available port`, `Select a modpack ZIP file`,
`Enter a modpack URL`, `Enter a pack name`, `Paste manifest JSON or upload a manifest file`,
`Select a target server`, `Enter a CurseForge API key first`, `Save pending changes before syncing`.

### 2.4 AWKWARD (c)

| File:line | Current | Proposed | Note |
|---|---|---|---|
| `routes/+page.svelte:315` | `Hardware utilization, game servers status, and network routing topology` | `Hardware utilization, server status, and routing topology` | "game servers status" grammar |
| `routes/+page.svelte:612` | `Instance telemetry & state across cluster` | `Server telemetry and state across the cluster` | missing article; "&"; "instance" (§3) |
| `routes/servers/+page.svelte:186` | `Manage, inspect, and deploy your Minecraft server instances` | `Manage, inspect, and deploy Minecraft servers` | "your", "instances" |
| `routes/servers/+page.svelte:377` | `Try adjusting your search criteria or status filter.` | `Adjust the search or status filter.` | "your" |
| `routes/servers/+page.svelte:787` and `routes/servers/[id]/+page.svelte:814` | `…The panel forgets this server. Its host port stays reserved. A proxy name is dropped the next time the panel starts.` | `…The server is removed from the panel. Its host port stays reserved; the proxy name is dropped at the next panel start.` | "forgets" anthropomorphic; two dialog copies differ (see §2.5 #96) |
| `routes/servers/[id]/+page.svelte:367` | fallback description `Minecraft Server Instance • Created …` | `Minecraft server • Created …` | Title Case fallback label |
| `routes/servers/[id]/+page.svelte:622` | `The last server reading is too old to confirm the current player count.` | `The last reading is too old to confirm the current player count.` | "server reading" odd |
| `routes/servers/new/+page.svelte:1162-1163` | `Provide server identification and select whether to build manual config or load from a curated modpack.` | `Name the server, then choose manual configuration or a modpack preset.` | "server identification" stiff; "build manual config" odd |
| `routes/servers/new/+page.svelte:1198` | `Start with clean Minecraft server settings and custom modloaders.` | `Start with vanilla settings and pick a mod loader.` | "clean"/"custom modloaders" (§3) |
| `routes/servers/new/+page.svelte:1313` | placeholder `Primary survival server instance for cluster...` | `Primary survival server for the cluster` | trailing ellipsis mid-phrase; "instance" |
| `routes/servers/new/+page.svelte:1540-1541` | `Configure port routing, reverse proxy domain, and a memory request (not a hard cap until the engine reports it as enforced).` | keep meaning, tighten: `Set port routing, proxy hostname, and a memory request. The limit is advisory until the engine reports it as enforced.` | ⚠ contract: `not a hard cap` pinned by `resource-enforcement.test.ts:124` — update test with the change |
| `routes/servers/new/+page.svelte:1997` | `Persists past daemon stop` (Detached Mode) | `Keeps running when the panel stops` | matches `server-settings.svelte:594`; see §2.5 #55 |
| `routes/servers/new/+page.svelte:2017` | `Start when host daemon starts` | `Start when the node starts` | "host daemon" jargon drift |
| `routes/servers/new/+page.svelte:1440` | label `Mod Loader / Server Software` | `Mod loader` | double term (§3) |
| `routes/servers/new/+page.svelte:1484` | `Supports runtime mod jar injection and server extensions.` | `Supports loading mods at runtime.` | "jar injection" dev-speak |
| `routes/library/[id]/+page.svelte:273` | `Immutable revision history, provenance, bindings and read-only compatibility findings.` | `Revision history, provenance, bindings, and read-only compatibility findings.` | "Immutable" is implied by §3 "revision"; Oxford comma |
| `routes/library/[id]/+page.svelte:492` | `Binding a revision journals a stop-based swap and gives the server a private writable clone.` | `Binding stops the server, swaps in the revision, and gives the server its own writable copy.` | "journals a stop-based swap" opaque |
| `routes/library/[id]/+page.svelte:785` | confirm body `Binding journals a stop-based swap. A running server is stopped, the level family is staged, published atomically, then restarted with a private writable clone. The immutable revision is never written to.` | `The server is stopped, the revision is published as the new level, and the server restarts with its own writable copy. The stored revision is never modified.` | same jargon, shorter |
| `routes/library/+page.svelte:219` | `Browse worlds on running servers and manage saved library revisions.` | fine | — (reference voice) |
| `routes/library/[id]/+page.svelte:715` | `The check is read-only. It reports detected, inferred and unknown findings with reasons; unknown results block binding.` | `Read-only check. Findings are detected, inferred, or unknown; unknown findings block binding.` | comma splice-ish, tighten |
| `routes/library/import/+page.svelte:123` | `Two distinct paths: API-backed content artifacts, or manual URL/file import of a prebuilt world.` | `Import from a content API, or from a URL or file upload.` | fragment style |
| `routes/library/import/+page.svelte:227` | `Choose a provider and project type, then search. Results come from the API; the panel does not fabricate a world catalog.` | `Choose a provider and project type, then search. Results come from the provider API.` | defensive meta-commentary; "fabricate" |
| `routes/library/new/+page.svelte:222` | `A version-driven, grouped wizard. Every control comes from the capability schema and states when it applies.` | `Fields are grouped by capability schema and state when they apply.` | "wizard describing itself" |
| `routes/library/new/+page.svelte:363` | `Set a target edition and Minecraft version, then reload the schema. Controls are never invented by the UI.` | `Set an edition and Minecraft version, then reload the schema.` | meta-commentary |
| `routes/players/[key]/+page.svelte:470` | `Sessions — Observed join and leave records.` | `Sessions — Observed joins and leaves.` | fine | — |
| `routes/settings/+page.svelte:123` | `Configure Carbon Panel and default server settings` | fine | — |
| `routes/servers/[id]/+page.svelte:638-639` | `First-boot modpack downloads (manifests, loader installers, mod jars) can take 2–5 minutes. Monitor real-time logs in the Console tab.` | `First-boot modpack downloads can take 2–5 minutes. Watch the Console tab.` | parenthetical padding |
| `lib/components/server-tasks.svelte:1797` | `Scheduled executions stop. Run history is kept. This cannot be undone.` | `Scheduled runs stop; run history is kept. This cannot be undone.` | three staccato fragments |
| `lib/components/server-datapacks.svelte:115` | `World data packs in datapacks/. They run on Vanilla servers with no mod loader.` | `Datapacks in the world's datapacks/ folder. They run on vanilla servers without a mod loader.` | "data packs" (§3); "Vanilla" casing |
| `routes/modpacks/studio/[id]/+page.svelte:1672` | `Search and install datapacks to bundle under the pack's datapacks/ override. Datapacks run on Vanilla servers with no mod loader.` | same replacement pattern as above | consistency |
| `lib/components/worldview/WorldViewWorkspace.svelte:835` | `Your account needs world_view/read on this server. No map frame is loaded for this session. Ask an administrator for access.` | `This account needs world_view/read on this server, so no map is loaded. Ask an administrator for access.` | "Your"; three fragments |
| `lib/components/worldview/WorldViewWorkspace.svelte:658` | `Permission denied: your account cannot run commands on this server. A map session cannot grant this.` | `Commands are not permitted for this account on this server. A map session cannot grant them.` | "your" |
| `lib/components/auth-settings.svelte:308` | `Modrinth API policy encourages client identification including application name and contact info.` | `Modrinth asks for a User-Agent that identifies the application and a contact address.` | stiff |
| `routes/modpacks/+page.svelte:760` | button `Use in Server` | `Create server` (goes to `/servers/new?modpack=`) or `Deploy…` | preposition-dropping awkwardness |
| `routes/login/+page.svelte:730 area` — `authTabs` label | `{ id: 'login', label: 'Login' }` | `Sign in` | §3 verb form; pair with §2.5 #32 |

### 2.5 INCONSISTENT TERMINOLOGY / CASING (d)

| # | Topic | Evidence (file:line) | Proposed |
|---|---|---|---|
| 32 | **Login vs Sign in** | tab `Login` (`routes/login/+page.svelte:257`), page title `Carbon Panel - Login` (:284), button `Sign In` (:429), `Sign in with SSO` (:454), toast `Logged in successfully` (:172) | tab `Sign in`, title `Carbon Panel — Sign in`, button `Sign in`, toast `Signed in` |
| 33 | **whitelist vs allowlist** | remaining UI-visible "Whitelist": `lib/components/server-configuration.svelte:548` (`Propagate configured default Ops and Whitelist…`), `:555` (`Sync Ops & Whitelist`); label uses canonical form already in `PlayerRoster.svelte:393` (`Allowlisted`), `presentation.ts:546` (`Add to allowlist`), `server-activity.ts:27-28` | replace both strings with **allowlist**; proto field names/labels (`ops_and_whitelist_only`, `SetWhitelistEntryRequest`) are API contracts — do not rename code, only visible copy |
| 34 | **ops vs operators** | `server-configuration.svelte:548,555` (`Ops`), `players/[key]:543` (`Op level`) | visible copy: `Operators`; keep `Op level` (Minecraft term) |
| 35 | **server vs instance** | `servers/+page.svelte:408` (header `Server Instance`), `:186` (`server instances`), `servers/[id]:367` (`Minecraft Server Instance`), `servers/new:1313`, dashboard `+page.svelte:612` | **server** everywhere; "instance" only when the Docker container specifically is meant |
| 36 | **engine / modloader / server software** | wizard step `Game Engine` (`servers/new:92`), header `Engine & Loader` (`servers/+page.svelte:409`), `Engine specs` (`servers/[id]:488`), label `Mod Loader / Server Software` (`servers/new:1440`), `custom modloaders` (`servers/new:1198`), `getModLoaderDisplay` column, `ModpackDetails.svelte` | canonical **mod loader** (two words) for Fabric/Forge/etc.; the Minecraft release row is **Minecraft version**. Replace "Engine" headings: step `Runtime`, list header `Minecraft & loader`, detail heading `Runtime` |
| 37 | **datapack vs data pack** | `server-datapacks.svelte:115` (`World data packs`), everywhere else `datapack` | **datapack** |
| 38 | **map view / World View / World map** | tab `World` (`servers/[id]:109`), modal title `World map` (`servers/[id]:878`), card `World View preview` (`WorldViewWorkspace.svelte:693`), empty `Map not available` (:1056), library link `View map` (`library/+page.svelte:356`) | canonical **World view** for the feature, **Map** for the canvas; card → `World view`, keep modal `World map` |
| 39 | **Save vs Apply vs Stage** | configuration `Stage` (`server-configuration.svelte:591`), tasks `Save Changes` (`server-tasks.svelte:1706`), mods `Stage this update` (pinned; removed by the PR #729 split), server settings implicit save | keep: **Stage** = queue changes with an apply window; **Save** = write settings now; document in §3 and leave as-is (they are different actions) |
| 40 | **ellipsis character** | mixed ASCII `...` (`servers/+page.svelte:215`, `modpacks/+page.svelte:534`, `CarbonInlineLoading.svelte:17`, many) and Unicode `…` (`library` pages, `players` pages, `servers/new:1056,1070,1078`) | standardize on `…` (U+2026) in visible copy; includes the login P0 fix |
| 41 | **& vs and** | `Mod & Plugin Management` (`server-mods.svelte:558`), `Modules & Sidecars` (`modules/+page.svelte:32`), `Engine & Loader` (`servers/+page.svelte:409`), `Library entries` table desc `provenance, bindings and compatibility` (`library/+page.svelte:422`) | **and** in body copy; `&` acceptable only in nav labels — pick nav-only rule and apply |
| 42 | **button casing** | Title Case: `Create Server`, `Import Modpack`, `Create Admin Account`, `Open Console`, `Delete Server`, `Restore Backup`, `Delete Node` vs sentence case: `Open server` (`servers/new:1088`), `Back to servers`, `Create from config`, `Import world`, `Bind revision` | **sentence case** for all buttons (Carbon style); batch find/replace in confirm `confirmLabel`s too |
| 43 | **label casing** | `Max Players` (`servers/new:1724`) vs `Memory request (MB)` (`:1700`); `Minecraft Version` vs `Minecraft version` across pages; `Token Name *`, `Expiration Duration` (`profile:656,664`) | sentence case for field labels |
| 44 | **Wake/Resume/Start/Stop action titles** | dashboard uses `Wake Server`/`Start Server`/`Resume Server`/`Stop Server` (`+page.svelte:747-791`) while list page uses `Wake`/`Resume`/`Start` (`servers/+page.svelte:522-526`) | single verb set: `Wake`, `Resume`, `Start`, `Stop` (aria-labels keep the server name) |
| 45 | **empty-state default** | `CarbonEmptyState.svelte:17` default `Nothing here yet` | `Nothing to show` or require callers to pass a title (preferred) |
| 46 | **delete-confirm copy differs for the same dialog** | list page `servers/+page.svelte:790` (`This action cannot be undone. The container and the server files are removed.`) vs detail `servers/[id]/+page.svelte:817` (`This will stop and remove the container, delete all files and data, and remove mods and configurations. This action cannot be undone.`) | use the detail-page (fuller) text in both places |
| 47 | **no-nodes message differs** | `servers/new:455` (`No active Docker nodes available — register a node in Settings → Docker Nodes first.`) vs `:1841-1843` (`No worker nodes available. Add and enable a Docker node in Settings → Docker Nodes before creating servers.`) vs `modpacks/+page.svelte:387` | single string: `No active Docker nodes. Register one in Settings → Docker Nodes first.` |
| 48 | **Detached-mode description** | wizard `Persists past daemon stop` (`servers/new:1997`) vs settings `Server continues running when Carbon Panel stops` (`server-settings.svelte:594`) | one sentence, used in both: `Server keeps running when Carbon Panel stops` |
| 49 | **"panel" self-reference** | `The panel returned no server.` (`servers/new:760,776,996`), `the panel does not fabricate…` (`library/import:227`), `this panel first learned` (`players/[key]:414`), `The panel forgets this server` (`servers/+page:787`) | acceptable shorthand; keep `the panel`, remove anthropomorphic verbs ("forgets", "learned of" → "first observed") |
| 50 | **`Loading` descriptions style** | `Loading server` + `Connecting to server daemon...` (`servers/[id]:347`), `Loading supported versions catalog...` (`servers/new:1402`), `Loading the version-driven schema...` (`library/new:359`) | pattern `Loading <thing>…`; drop editorializing second sentences where the spinner is self-evident |
| 51 | **sidebar/footer vs page names** | nav `Overview` (`CarbonShell.svelte:228`) vs page `Cluster Overview` (`+page.svelte:314`); nav `Modpacks` vs page header `Modpacks` ok; nav `Modules & Sidecars` vs page `Modules & Sidecars` ok | rename nav to `Dashboard` or page to `Overview` (pick one; proposed: nav `Dashboard`, page keeps `Cluster Overview`) |

---

## 3. Terminology table (canonical)

| Concept | Canonical term | Non-canonical variants found (files) |
|---|---|---|
| Access list for players | **allowlist** | `Whitelist` in `lib/components/server-configuration.svelte:548,555` (UI copy only; proto fields `ops_and_whitelist_only`, `SetWhitelistEntryRequest` are API names and stay) |
| Server admins | **operators** (verb: grant/remove operator) | `Ops` in `server-configuration.svelte:548,555`; `Op level` is fine (Minecraft term) |
| A managed Minecraft server | **server** | `instance` / `Server Instance` / `Minecraft Server Instance` in `servers/+page.svelte:186,408`, `servers/[id]/+page.svelte:367`, `+page.svelte:612`, `servers/new:1313`; `worker node` (keep **node** for Docker hosts) |
| Fabric/Forge/NeoForge/Quilt/Paper… | **mod loader** | `modloaders` (`servers/new:1198`), `Mod Loader / Server Software` (`servers/new:1440`), `loader` alone in library forms (`library/new:257`, `library/import:189` — acceptable short form inside a form already titled by context), `Engine` headings (`servers/+page.svelte:409`, `servers/[id]:488`, `servers/new:92`) |
| Minecraft release | **Minecraft version** | `MC version` (`pack-content-panel.svelte:861` placeholder), `MC ${v}` tags (`servers/new:1224,2471` — acceptable mono-tag shorthand), `game version` (`servers/new:292`) |
| World data additions | **datapack** | `data packs` (`server-datapacks.svelte:115`, `studio/[id]:1672`) |
| The map feature | **World view** (feature), **map** (canvas/dialog) | `World map` modal (`servers/[id]:878` is fine), `Map not available` (fine), `World View preview` card title (`WorldViewWorkspace.svelte:693` → `World view preview`) |
| Queued changes with an apply window | **stage / staged** (then **apply**) | consistent already; do not replace with "save" |
| Write-now settings | **Save** | `Save Changes` (`server-tasks.svelte:1706`) — keep |
| Enable/disable toggles | **enable / disable** | `Disable` action (`server-tasks.svelte:1036`) ok; avoid "turn off/on" |
| Plugins vs mods | **mods** for Fabric/Forge content, **plugins** for Paper/Spigot/Bukkit | handled contextually by `server-mods.svelte:528-548` directory map — keep the current per-loader directory naming |
| Authentication verb | **sign in** (noun: sign-in) | `Login` tab/title, `Logged in successfully` (`login/+page.svelte:172,257,284`) |
| Panel self-reference | **the panel** | consistent; remove anthropomorphic uses ("forgets") |
| Docker hosts | **node** (Docker node) | `worker node` (`servers/new:1788,1841`), `host machine` (`:1809`) |
| Token secret | **token** | `secret` (`profile/+page.svelte:617`) |

---

## 4. Additional observed issues (status text, toasts, aria)

- `lib/components/server-console.svelte:285,396` toast `Command executed` — fine; `:355` `Viewed buffer cleared` — contract-pinned phrase family, leave.
- `lib/components/server-operations.svelte:227-231` empty text is **pinned** (§6); do not change without test update.
- `lib/components/server-mods.svelte:690` status `Checking installed mods for updates` — good pattern for progress rows; reuse.
- `lib/components/server-mods.svelte:967` title `Click to disable` — "Click" → `Disable`.
- `lib/components/player-avatar.svelte` / `PlayerLookup.svelte:334` `Unavailable` — fine.
- `routes/modpacks/+page.svelte:720` `Loader not listed` — fine.
- `lib/components/manifest-inspector-dialog.svelte:123` title `CurseForge and Modrinth Manifest Inspector` — long; `Inspect modpack manifest`.
- `lib/components/mod-browser-dialog.svelte:581` title `Browse & Install Online Mods` → `Browse and install mods`.
- `lib/components/mod-browser-dialog.svelte:305` `Please verify server connection.` → `Check the server connection and retry.`
- `routes/modpacks/studio/+page.svelte:319` description `Visual workspace for custom modpacks: create, import (.mrpack, CurseForge, Packwiz), edit overrides, and deploy.` — "Visual" is marketing; → `Workspace for custom modpacks: create, import (.mrpack, CurseForge, Packwiz), edit overrides, and deploy.`
- `routes/modpacks/studio/[id]/+page.svelte:691` export button title `Export .mrpack, CurseForge, instance.zip or Packwiz archive` — fine.
- `routes/servers/+page.svelte:444` `No description provided` — fine.
- `routes/servers/new/+page.svelte:1397` readonly value `Set by the selected modpack release` — good.
- `routes/servers/new/+page.svelte:1480` `Vanilla Minecraft server binary with no mod dependencies.` → `Vanilla server with no mods.` (P3)
- Dashboard system-health `RPC telemetry / NOT MEASURED` — fine.
- `lib/components/node-settings.svelte:1410-1412` delete confirm — fine after casing pass.
- `routes/modpacks/studio/export-dialog.svelte:84` `Choose a launcher format and per-launcher options.` — fine.
- aria-labels: no user-visible oddities found except casing items in §2.5 #42/#44 (`aria-label="Copy Token"` vs `Copy token`, `aria-label="Delete token"` vs title `Delete token` already consistent).

---

## 5. Scope summary

| Area | Files touched by replacements | Approx. strings |
|---|---|---|
| Routes (13 pages) | 13 | ~45 |
| `lib/components` (server tabs, settings, worldview, files) | 24 | ~35 |
| `lib/features` + label maps (`presentation.ts`, `mod-update-ui.ts` (removed by the PR #729 split; retained summary is `lib/utils/mod-update-summary.ts`), `server-join.ts`, `overlays.ts`) | 6 | ~12 |
| Carbon shared components (empty-state default, inline-loading default) | 2 | 2 |

---

## 6. Prioritized implementation plan

### P0 — defects and dangerous/critical copy (2 strings, 2 files)
1. `routes/login/+page.svelte:327` — replace the U+FFFD byte sequence with `…` (encoding fix, not just a character swap).
2. Detach-dialog wording fix at both call sites (`servers/+page.svelte:787`, `servers/[id]/+page.svelte:814`) **and** unify the two delete-dialog bodies (#46) in the same edit — these are destructive-action descriptions.

No contract tests pin these; `server-console.test.ts` does *not* pin the delete dialogs.

### P1 — primary flows: dashboard, servers, create wizard, mods, players (24 strings, 11 files)
- Dashboard header/subtitle grammar (`+page.svelte:315,612`), empty states (`:633`).
- Servers list: header `Server Instance` → `Server`, `Engine & Loader` → `Minecraft & loader`, empty-state copy, action titles (#44).
- Server detail: `Engine specs` → `Runtime`, description fallback, starting/creating banners.
- Create wizard: step title mismatches (`steps` array vs `Step N:` headings), `Mod Loader / Server Software`, modpack tile copy, node placement tiles, lifecycle toggle descriptions (#47/#48), "Please" toasts.
- Mods tab: `Mod & Plugin Management` casing pass; `Click to disable`.
- Players: roster filter labels already canonical; fix `AddAllowlistEntry` button casing only.

Contract tests that must be updated in this group: none for the listed strings; note
`server-mods-update.test.ts` (removed by the PR #729 split) pinned several mods-tab strings — those
strings are *not* in the P1 change set (they are already canonical), so no test edits expected. The
retained `mod-update-summary.test.ts` is unaffected.

### P2 — secondary pages (41 strings, ~20 files)
- World Library pages (new/import/[id]) jargon rewrites (§2.4), ellipsis normalization.
- Modpacks + Studio + export dialog (exclamations, "Please" removals, `Use in Server`).
- Settings area (auth, api-keys, nodes, roles, users, routing, logs, support): exclamation removal, "Please" removal, label casing, `Enable proxy system` → `Enable proxy`.
- Modules pages (`Modules & Sidecars` copy kept; button casing).
- `docs/api` description (drop `make gen`), `+error.svelte` title, login recovery link.
- Preferences dialog, manifest inspector, modpack deploy dialog.

Contract tests to update in this group:
- `error-states.test.ts:109` pins `Settings unavailable` — **keep the string** (only its message body changes), no test edit.
- `routes/profile/profile-dialogs.test.ts:36,37,45,61` pin `Manage profile`, `Roles`, `Update password`, `Reset defaults` — keep these exact strings.
- `resource-enforcement.test.ts:124` pins `not a hard cap` on the create page — if §2.4 row for `servers/new:1540` is applied, update the test to the new phrase (`advisory`); it also asserts `resource quotas` is absent (keep that assertion true).

### P3 — toasts, helpers, casing sweeps (25 strings, ~18 files)
- `X successfully` → `X` (13 toasts, list in §2.3).
- Remaining `Please` removals in low-traffic validation paths.
- Ellipsis normalization `...` → `…` across visible copy (mechanical, exclude code/URLs).
- `&` → `and` in body copy (keep `&` in nav labels per rule in §2.5 #41).
- Button/label sentence-case sweep (Title Case → sentence case) including confirm `confirmLabel`s.
- `CarbonEmptyState.svelte:17` default title `Nothing here yet` → `Nothing to show`.

Contract tests to update in this group:
- `server-console.test.ts:36,41,49,123,127` pin `title="Upload logs to mclo.gs"`, `title="Download raw log"`, `viewed buffer`, `viewed buffer only`, `Daemon logs are kept` — **do not change** these console strings.
- `server-operations.test.ts:81` pins `No operations yet. Start, stop or restart to populate this journal.` — do not change.
- `a11y-audit.test.ts:97,101` pin `title="Restore backup?"` / `title="Delete backup?"` — do not change.
- `file-tree.test.ts:20` pins `Modified` header — do not change.
- `server-backups.test.ts:17` pins `Back up now` — do not change.
- `ingress-connect.test.ts:78,82` pin `Upload local datapack`, `Local upload` — do not change.
- `mod-browser-dialog.test.ts:30,68,87,107,111` pin `Install with all dependencies`, `Install`, `Added this session`, `{n} added`, `Installed`, `Added just now` — do not change.
- `pack-content-panel.test.ts:20-22` pin `label: 'Datapacks'/'Resource Packs'/'Shaders'` — do not change.
- `server-status.test.ts:59` pins `Server running but failing health checks` — do not change.
- `dashboard-tiles.test.ts:49` pins `describeNodeStatusLabel` (ONLINE/OFFLINE labels) — do not change.
- `world-library/routes.contract.test.ts:95` pins the wizard group labels in `library/new` — do not change group titles.

### Execution notes for the implementation agent
1. Work file-by-file; the fixes are independent per file but §2.5 pairs (#46, #47, #48, #32) must land together to avoid new inconsistencies.
2. Run `cd web/carbon-panel && bun run check && bun run test` after each P-group; `bun run test` is the gate for the pinned strings above.
3. The U+FFFD fix must be a byte-level edit (file already contains other valid UTF-8; only that one byte sequence changes).
4. Do not rename proto-generated identifiers (`SetWhitelistEntryRequest`, `opsAndWhitelistOnly`, `whitelisted` fields) — visible copy only.
5. After landing, re-grep for regressions: `Please |successfully|Welcome!|whitelist` (case-insensitive) should return only test files, API identifiers, and the intentionally pinned strings listed above.
