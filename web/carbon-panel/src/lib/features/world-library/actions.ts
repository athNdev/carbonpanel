// SPDX-License-Identifier: MPL-2.0
import type {
	BindWorldResponse,
	CompatibilityReport,
	ConfigDraftPreview,
	ProviderCapability,
	WorldArtifact,
	WorldConfigSchema,
	WorldEntry,
	WorldImportJob,
	WorldRevision
} from '../../proto/carbonpanel/v1/world_pb';
import type { WorldLibraryInventory } from './inventory.svelte';

export type ActionOutcome<K extends string, V> = { kind: K; value: V } | { kind: 'superseded' };

export interface ImportWorldInput {
	source: 'url' | 'upload' | 'backup';
	url?: string;
	uploadId?: string;
	displayName: string;
	sourcePlatform?: string;
	licenseAttestation?: string;
	sha256Checksum?: string;
	requestId: string;
	entryId?: string;
}

export interface CheckCompatibilityInput {
	revisionId: string;
	serverId: string;
	targetMcVersion: string;
	/** Optional local level context; part of the action identity when supplied. */
	levelName?: string;
}

export interface BindWorldInput {
	serverId: string;
	levelName: string;
	revisionId: string;
	window: 'now' | 'next_restart' | 'idle';
	requestId: string;
	entryId?: string;
}

export interface UnbindWorldInput {
	serverId: string;
	levelName: string;
	requestId: string;
	entryId?: string;
}

export interface ResolveOperationInput {
	serverId: string;
	operationId: string;
	action: 'recover' | 'rollback';
	note?: string;
	entryId?: string;
}

export interface ConfigSchemaInput {
	edition: string;
	mcVersion: string;
	modLoader: number;
	dataVersion: string;
}

export interface PreviewConfigInput extends ConfigSchemaInput {
	values: Record<string, string>;
	preset?: string;
}

export interface CreateWorldInput extends PreviewConfigInput {
	name: string;
	licenseAttestation?: string;
	requestId: string;
}

export interface SearchArtifactsInput {
	provider: 'modrinth' | 'curseforge';
	query: string;
	mcVersion: string;
	modLoader: number;
	projectType: string;
	limit?: number;
	cursor?: string;
}

export interface ListBindingsInput {
	serverId?: string;
	revisionId?: string;
}

export interface ArtifactSearchResult {
	artifacts: WorldArtifact[];
	nextCursor: string;
	rateLimited: boolean;
}

export interface CreateWorldResult {
	entry?: WorldEntry;
	revision?: WorldRevision;
}

export interface WorldLibraryActionRequests {
	importWorld(input: ImportWorldInput): Promise<{ job?: WorldImportJob; revision?: WorldRevision }>;
	checkCompatibility(input: CheckCompatibilityInput): Promise<CompatibilityReport | undefined>;
	bindWorld(input: BindWorldInput): Promise<BindWorldResponse>;
	unbindWorld(input: UnbindWorldInput): Promise<{ removed: boolean }>;
	resolveWorldOperation(input: ResolveOperationInput): Promise<{ outcome: string }>;
	getWorldConfigSchema(input: ConfigSchemaInput): Promise<WorldConfigSchema | undefined>;
	previewWorldConfig(input: PreviewConfigInput): Promise<ConfigDraftPreview | undefined>;
	createWorldFromConfig(input: CreateWorldInput): Promise<CreateWorldResult>;
	searchWorldArtifacts(input: SearchArtifactsInput): Promise<ArtifactSearchResult>;
	getProviderCapabilities(): Promise<ProviderCapability[]>;
	getRevision(revisionId: string): Promise<WorldRevision | undefined>;
	listRevisions(entryId: string): Promise<WorldRevision[]>;
	listBindings(input: ListBindingsInput): Promise<WorldBindingView[]>;
	listImportJobs(entryId: string): Promise<WorldImportJob[]>;
}

/** Bindings are displayed with the revision they point at; no world bytes travel. */
export interface WorldBindingView {
	id: string;
	serverId: string;
	levelName: string;
	revisionId: string;
	nodeId: string;
	generation: bigint;
	consistency: string;
}

const key = (...parts: Array<string | number | boolean | undefined | null>) =>
	parts.map((part) => String(part ?? '')).join('|');

/**
 * Typed outcomes keep confirmation, navigation and toasts in the views. Reads
 * and writes share generation fencing: a response from a reset inventory can
 * never update a newer screen. An uncertain bind is reported, never retried.
 */
export function createWorldLibraryActions(
	requests: WorldLibraryActionRequests,
	inventory: Pick<WorldLibraryInventory, 'invalidate' | 'generation'>
) {
	const pending = new Map<string, Promise<ActionOutcome<string, unknown>>>();

	function perform<K extends string, V>(
		kind: K,
		operation: string,
		dedupKey: string,
		execute: (active: () => boolean) => Promise<V>,
		after?: (value: V) => void
	): Promise<ActionOutcome<K, V>> {
		const session = inventory.generation();
		const active = () => inventory.generation() === session;
		const fullKey = `${session}:${operation}:${dedupKey}`;
		const existing = pending.get(fullKey);
		if (existing) return existing as Promise<ActionOutcome<K, V>>;

		const run = (async (): Promise<ActionOutcome<K, V>> => {
			const value = await execute(active);
			if (!active()) return { kind: 'superseded' };
			after?.(value);
			return { kind, value };
		})()
			.catch((error): ActionOutcome<K, V> => {
				if (!active()) return { kind: 'superseded' };
				throw error;
			})
			.finally(() => {
				if (pending.get(fullKey) === run) pending.delete(fullKey);
			});
		pending.set(fullKey, run as Promise<ActionOutcome<string, unknown>>);
		return run;
	}

	const invalidateEntry = (id: string | undefined) => {
		if (id) inventory.invalidate(id);
	};

	return {
		importWorld(input: ImportWorldInput) {
			return perform(
				'imported',
				'import',
				key(input.requestId, input.source, input.uploadId, input.url),
				() => requests.importWorld(input),
				(result) => invalidateEntry(result.job?.worldEntryId || input.entryId)
			);
		},
		checkCompatibility(input: CheckCompatibilityInput) {
			return perform(
				'checked',
				'compat',
				key(input.revisionId, input.serverId, input.targetMcVersion, input.levelName),
				() => requests.checkCompatibility(input)
			);
		},
		bindWorld(input: BindWorldInput) {
			return perform(
				'bound',
				'bind',
				key(input.requestId, input.serverId, input.levelName, input.revisionId),
				() => requests.bindWorld(input),
				() => invalidateEntry(input.entryId)
			);
		},
		unbindWorld(input: UnbindWorldInput) {
			return perform(
				'unbound',
				'unbind',
				key(input.requestId, input.serverId, input.levelName),
				() => requests.unbindWorld(input),
				() => invalidateEntry(input.entryId)
			);
		},
		resolveOperation(input: ResolveOperationInput) {
			return perform(
				'resolved',
				'resolve',
				key(input.operationId, input.action, input.note),
				() => requests.resolveWorldOperation(input),
				() => invalidateEntry(input.entryId)
			);
		},
		getWorldConfigSchema(input: ConfigSchemaInput) {
			return perform(
				'schema',
				'schema',
				key(input.edition, input.mcVersion, input.modLoader, input.dataVersion),
				() => requests.getWorldConfigSchema(input)
			);
		},
		previewWorldConfig(input: PreviewConfigInput) {
			return perform(
				'previewed',
				'preview',
				key(
					input.edition,
					input.mcVersion,
					input.modLoader,
					input.dataVersion,
					input.preset,
					JSON.stringify(input.values)
				),
				() => requests.previewWorldConfig(input)
			);
		},
		createWorld(input: CreateWorldInput) {
			return perform(
				'created',
				'create',
				key(input.requestId, input.name),
				() => requests.createWorldFromConfig(input),
				(result) => invalidateEntry(result.entry?.id)
			);
		},
		searchWorldArtifacts(input: SearchArtifactsInput) {
			return perform(
				'searched',
				'artifacts',
				key(
					input.provider,
					input.query,
					input.mcVersion,
					input.modLoader,
					input.projectType,
					input.cursor
				),
				() => requests.searchWorldArtifacts(input)
			);
		},
		getProviderCapabilities() {
			return perform('capabilities', 'capabilities', '', () => requests.getProviderCapabilities());
		},
		getRevision(revisionId: string) {
			return perform('revision', 'revision', key(revisionId), () =>
				requests.getRevision(revisionId)
			);
		},
		listRevisions(entryId: string) {
			return perform('revisions', 'revisions', key(entryId), () => requests.listRevisions(entryId));
		},
		listBindings(input: ListBindingsInput) {
			return perform('bindings', 'bindings', key(input.serverId, input.revisionId), () =>
				requests.listBindings(input)
			);
		},
		listImportJobs(entryId: string) {
			return perform('jobs', 'jobs', key(entryId), () => requests.listImportJobs(entryId));
		}
	};
}

export type WorldLibraryActions = ReturnType<typeof createWorldLibraryActions>;
