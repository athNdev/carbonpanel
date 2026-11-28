// SPDX-License-Identifier: MPL-2.0

// Shared state machine for the mods browser (catalog → detail → install →
// session), instantiated per mount — server tab, wizard step and any future
// host each own an instance. See docs/plans/mods-ui-unification.md §4 for the
// region map and the edge-case coverage this encodes:
//
// - Stale catalog/versions responses are dropped by sequence guards (E15).
// - `closeDialog` resets the install region including `installing`, so a
//   reopen during an in-flight install never shows a silently disabled
//   button (E7). Closing does not drop the query (E12, intentional).
// - `setTarget` is the TARGET_CHANGED event: a hard reset that also
//   invalidates every in-flight sequence (E22).
// - `selectResult` is refused while an install is running, keeping the
//   selection for the retryable installError state.
// - Install outcomes are per item (E10); unresolved dependencies from
//   provider outages are surfaced through `appendOutcomes` instead of being
//   silently dropped (E9).
// - Install responses carry their own sequence token: a response that
//   resolves after a close/reopen or once a newer install started is
//   dropped wholesale, so it can never overwrite the newer install's
//   outcomes, session entry or `installing` flag.
//
// Only `$state` runes are used inside the factory (no `$derived`/`$effect`)
// so tests can mock the rune as an identity and drive the machine directly.

import {
	MAX_DEPENDENCY_DEPTH,
	buildDependencyPlan,
	type DependencyPlanResult,
	type InstallItem,
	type PlanDependency,
	type PlanVersion
} from './dependency-plan';

export type ModsCatalogPlatform = 'modrinth' | 'curseforge';

export interface ModsBrowserTarget {
	/** `server` installs directly; `wizard` queues into a create-server plan. */
	kind: 'server' | 'wizard';
	id: string;
	loader: string;
	mcVersion: string;
	/**
	 * Compatible-side filter the first catalog request must share with later
	 * searches. Omitted for providers without client/server sides (Hangar
	 * plugins), so the opener never invents a filter the provider cannot honor.
	 */
	side?: string;
}

export interface ModsCatalogResult {
	id: string;
	slug: string;
	title: string;
	description: string;
	iconUrl: string;
	author: string;
	downloads: number;
	categories: string[];
	platform: string;
	clientSide?: string;
	serverSide?: string;
	installed: boolean;
}

export type ModsCatalogVersion = PlanVersion;

export type OutcomeTone = 'green' | 'yellow' | 'red' | 'gray';

export interface InstallOutcomeRow {
	key: string;
	label: string;
	tone: OutcomeTone;
	/** Overrides the tone-derived badge text (Installed/Failed/Skipped). */
	tag?: string;
	detail?: string;
}

export interface SessionInstallEntry {
	key: string;
	title: string;
	version: string;
	depCount: number;
	unresolvedCount: number;
}

export interface CatalogSearchRequest {
	serverId: string;
	query: string;
	platform: string;
	loader: string;
	mcVersion: string;
	side?: string;
}

/** Transport seam injected by the host component (rpcClient in production). */
export interface ModsBrowserPorts {
	search(request: CatalogSearchRequest): Promise<{ results: ModsCatalogResult[] }>;
	versions(request: {
		serverId: string;
		project: string;
		platform: string;
		loader: string;
		mcVersion: string;
	}): Promise<{ versions: PlanVersion[] }>;
	install(request: {
		serverId: string;
		items: Array<{ name: string; fileName: string; downloadUrl: string }>;
	}): Promise<{ success: boolean; installed: string[]; errors: string[]; message: string }>;
}

function createSequenceGuard() {
	let latest = 0;
	return {
		next(): number {
			latest += 1;
			return latest;
		},
		isCurrent(seq: number): boolean {
			return seq === latest;
		},
		invalidate(): void {
			latest += 1;
		}
	};
}

export function createModsBrowserStore(ports: ModsBrowserPorts) {
	// Region: catalog
	let query = $state('');
	let platform = $state<ModsCatalogPlatform>('modrinth');
	let searching = $state(false);
	let results = $state<ModsCatalogResult[]>([]);
	let searchError = $state('');

	// Region: detail
	let selectedResult = $state<ModsCatalogResult | null>(null);
	let loadingVersions = $state(false);
	let versions = $state<ModsCatalogVersion[]>([]);
	let versionsError = $state('');
	let selectedVersion = $state<ModsCatalogVersion | null>(null);
	let dependencySelection = $state<Record<string, boolean>>({});

	// Region: install
	let installing = $state(false);
	let installOutcomes = $state<InstallOutcomeRow[]>([]);

	// Region: session
	let sessionInstalled = $state<SessionInstallEntry[]>([]);
	let sessionSummaryOpen = $state(true);

	// Region: target identity
	let target = $state<ModsBrowserTarget>({ kind: 'server', id: '', loader: '', mcVersion: '' });

	const searchSequence = createSequenceGuard();
	const versionsSequence = createSequenceGuard();
	const installSequence = createSequenceGuard();

	const sessionKeyFor = (result: { platform: string; id: string }): string =>
		`${result.platform}:${result.id}`;

	async function runSearch(filters?: {
		loader?: string;
		mcVersion?: string;
		side?: string;
	}): Promise<void> {
		const seq = searchSequence.next();
		searching = true;
		searchError = '';
		results = [];
		try {
			const response = await ports.search({
				serverId: target.id,
				query: query.trim(),
				platform,
				loader: filters?.loader ?? target.loader,
				mcVersion: filters?.mcVersion ?? target.mcVersion,
				side: filters?.side ?? target.side
			});
			if (!searchSequence.isCurrent(seq)) return;
			results = response.results;
		} catch (error) {
			if (!searchSequence.isCurrent(seq)) return;
			searchError =
				error instanceof Error && error.message
					? `Failed to search mods: ${error.message}`
					: 'Failed to search mods. Please verify the server connection.';
		} finally {
			if (searchSequence.isCurrent(seq)) searching = false;
		}
	}

	async function loadVersions(result: ModsCatalogResult): Promise<void> {
		const seq = versionsSequence.next();
		selectedResult = result;
		loadingVersions = true;
		versions = [];
		versionsError = '';
		selectedVersion = null;
		dependencySelection = {};
		const request = {
			serverId: target.id,
			project: result.platform === 'curseforge' && result.id ? result.id : result.slug || result.id,
			platform: result.platform,
			loader: target.loader,
			mcVersion: target.mcVersion
		};
		try {
			const response = await ports.versions(request);
			if (!versionsSequence.isCurrent(seq)) return;
			versions = response.versions;
			if (versions.length > 0) selectVersion(versions[0]);
		} catch (error) {
			if (!versionsSequence.isCurrent(seq)) return;
			versions = [];
			versionsError =
				error instanceof Error && error.message
					? error.message
					: 'Failed to load compatible versions for this mod';
		} finally {
			if (versionsSequence.isCurrent(seq)) loadingVersions = false;
		}
	}

	function selectVersion(version: ModsCatalogVersion): void {
		selectedVersion = version;
		dependencySelection = {};
		for (const dep of version.dependencies || []) {
			if (!dep.project_id) continue;
			if ((dep.dependency_type || '').toLowerCase() === 'required') {
				dependencySelection[dep.project_id] = true;
			}
		}
	}

	return {
		// ---- reactive state ----
		get query() {
			return query;
		},
		set query(value: string) {
			query = value;
		},
		get platform() {
			return platform;
		},
		get searching() {
			return searching;
		},
		get results() {
			return results;
		},
		get searchError() {
			return searchError;
		},
		get selectedResult() {
			return selectedResult;
		},
		get loadingVersions() {
			return loadingVersions;
		},
		get versions() {
			return versions;
		},
		get versionsError() {
			return versionsError;
		},
		get selectedVersion() {
			return selectedVersion;
		},
		get dependencySelection() {
			return dependencySelection;
		},
		get installing() {
			return installing;
		},
		get installOutcomes() {
			return installOutcomes;
		},
		get sessionInstalled() {
			return sessionInstalled;
		},
		get sessionCount() {
			return sessionInstalled.length;
		},
		get sessionSummaryOpen() {
			return sessionSummaryOpen;
		},
		set sessionSummaryOpen(value: boolean) {
			sessionSummaryOpen = value;
		},
		get target() {
			return target;
		},
		sessionKeyFor,

		// ---- events ----
		/** TARGET_CHANGED: hard reset; in-flight responses are dropped (E22). */
		setTarget(next: ModsBrowserTarget): void {
			target = next;
			query = '';
			platform = 'modrinth';
			results = [];
			searchError = '';
			searching = false;
			selectedResult = null;
			loadingVersions = false;
			versions = [];
			versionsError = '';
			selectedVersion = null;
			dependencySelection = {};
			installing = false;
			installOutcomes = [];
			sessionInstalled = [];
			sessionSummaryOpen = true;
			searchSequence.invalidate();
			versionsSequence.invalidate();
			installSequence.invalidate();
		},

		/**
		 * OPEN: refresh the catalog for the current target, using the same
		 * compatible-side context as a manual search.
		 */
		openDialog(): Promise<void> {
			return runSearch();
		},

		/** CLOSE: reset detail + install (E7); keep query/platform/results (E12). */
		closeDialog(): void {
			selectedResult = null;
			loadingVersions = false;
			versions = [];
			versionsError = '';
			selectedVersion = null;
			dependencySelection = {};
			installing = false;
			installOutcomes = [];
			sessionSummaryOpen = true;
			versionsSequence.invalidate();
			installSequence.invalidate();
		},

		/** SUBMIT_SEARCH — the sequence guard drops superseded responses (E15). */
		submitSearch(filters?: { loader?: string; mcVersion?: string; side?: string }): Promise<void> {
			return runSearch(filters);
		},

		/** PLATFORM_CHANGED: switch and re-search under one sequence. */
		setPlatform(next: ModsCatalogPlatform): Promise<void> {
			platform = next;
			return runSearch();
		},

		/** BACK/CLOSE of the version detail; outcomes and session are kept. */
		closeDetail(): void {
			selectedResult = null;
			loadingVersions = false;
			versions = [];
			versionsError = '';
			selectedVersion = null;
			dependencySelection = {};
			versionsSequence.invalidate();
		},

		/** Wizard-mode installs drive the same installing flag around their plan. */
		beginInstall(): void {
			installing = true;
		},

		endInstall(): void {
			installing = false;
		},

		/** SELECT_RESULT (guard: refused while installing so selection survives). */
		selectResult(result: ModsCatalogResult): Promise<void> {
			if (installing) return Promise.resolve();
			return loadVersions(result);
		},

		/** SELECT_VERSION: reset deps and pre-select required ones. */
		selectVersion,

		/** TOGGLE_DEPENDENCY (required rows are locked by the caller's UI). */
		toggleDependency(projectId: string, checked: boolean): void {
			dependencySelection = { ...dependencySelection, [projectId]: checked };
		},

		/**
		 * Resolve the selected version's dependencies into install items.
		 * Returns the plan so the host can surface unresolved/auto-added deps;
		 * folded-in transitive requirements are reflected in the selection (E8).
		 */
		async planDependencies(options: {
			version: ModsCatalogVersion;
			platform: string;
			loader: string;
			mcVersion: string;
			isKnownProject?: (projectId: string) => boolean;
			maxDepth?: number;
		}): Promise<DependencyPlanResult> {
			const plan = await buildDependencyPlan({
				deps: (options.version.dependencies || []) as PlanDependency[],
				platform: options.platform,
				loader: options.loader,
				mcVersion: options.mcVersion,
				selection: { ...dependencySelection },
				isKnownProject: options.isKnownProject,
				getVersions: (request) => ports.versions({ serverId: target.id, ...request }),
				maxDepth: options.maxDepth ?? MAX_DEPENDENCY_DEPTH
			});
			dependencySelection = plan.selection;
			return plan;
		},

		/**
		 * INSTALL_RESOLVED for the server mode: run the install and turn the
		 * response into per-item outcome rows (E10). Unresolved dependencies
		 * are appended by the host from the dependency plan (E9).
		 */
		async installServerItems(options: {
			items: InstallItem[];
			root: { platform: string; id: string; title: string; version: string; depCount: number };
			/**
			 * File the root mod itself installs as. The server's `installed`
			 * list may contain only dependencies, which must never be recorded
			 * as the selected mod being installed.
			 */
			rootFileName: string;
			unresolvedCount?: number;
		}): Promise<{ success: boolean; message: string; errors: string[] }> {
			const seq = installSequence.next();
			installing = true;
			try {
				const response = await ports.install({
					serverId: target.id,
					items: options.items.map((item) => ({
						name: item.name,
						fileName: item.file_name,
						downloadUrl: item.download_url
					}))
				});
				if (!installSequence.isCurrent(seq)) {
					// A close/reopen or a newer install owns the install region;
					// the stale response is dropped wholesale instead of
					// overwriting its outcomes, session entry or installing flag.
					return {
						success: response.success,
						message: response.message,
						errors: response.errors
					};
				}
				// eslint-disable-next-line svelte/prefer-svelte-reactivity -- Local non-reactive lookup, discarded after mapping outcomes.
				const installedFiles = new Set(response.installed);
				// The server reports installed files by the base name of the trimmed
				// request name (`filepath.Base(strings.TrimSpace(...))`), so an item
				// or the selected version's name may be padded or path-bearing and
				// still match. The raw name stays in the failure message.
				const installedName = (fileName: string): string => {
					const trimmed = fileName.trim();
					return trimmed.split(/[\\/]/).pop() || trimmed;
				};
				const isInstalled = (fileName: string): boolean =>
					installedFiles.has(installedName(fileName));
				const rootInstalled = isInstalled(options.rootFileName);
				const succeeded = response.success && rootInstalled;
				// The server names each failed item inside its error strings
				// ("skipped <name>: …"); attribute per item when possible and
				// keep the blanket errors[0] only for a single failed item.
				const failedItems = options.items.filter((item) => !isInstalled(item.file_name));
				const detailFor = (item: InstallItem): string =>
					response.errors.find((message) => message.includes(item.name)) ??
					(failedItems.length === 1 ? response.errors[0] : undefined) ??
					'Not installed';
				installOutcomes = options.items.map((item) =>
					isInstalled(item.file_name)
						? { key: item.file_name, label: item.name, tone: 'green' as const }
						: {
								key: item.file_name,
								label: item.name,
								tone: 'red' as const,
								detail: detailFor(item)
							}
				);
				if (succeeded) {
					const key = sessionKeyFor(options.root);
					sessionInstalled = [
						...sessionInstalled.filter((entry) => entry.key !== key),
						{
							key,
							title: options.root.title,
							version: options.root.version,
							depCount: options.root.depCount,
							unresolvedCount: options.unresolvedCount ?? 0
						}
					];
					sessionSummaryOpen = true;
				}
				return {
					success: succeeded,
					message:
						response.success && !rootInstalled
							? `The selected mod file ${options.rootFileName} was not installed.`
							: response.message,
					errors: response.errors
				};
			} finally {
				if (installSequence.isCurrent(seq)) installing = false;
			}
		},

		/** Append rows (e.g. unresolved dependencies, E9) to the outcome list. */
		appendOutcomes(rows: InstallOutcomeRow[]): void {
			installOutcomes = [...installOutcomes, ...rows];
		},

		clearOutcomes(): void {
			installOutcomes = [];
		}
	};
}

export type ModsBrowserStore = ReturnType<typeof createModsBrowserStore>;
