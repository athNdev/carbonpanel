// SPDX-License-Identifier: MPL-2.0
import { create } from '@bufbuild/protobuf';
import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
import type { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
import {
	BindWorldRequestSchema,
	CheckCompatibilityRequestSchema,
	CreateWorldFromConfigRequestSchema,
	GetProviderCapabilitiesRequestSchema,
	GetWorldConfigSchemaRequestSchema,
	GetWorldEntryRequestSchema,
	ImportWorldRequestSchema,
	ListBindingsRequestSchema,
	ListImportJobsRequestSchema,
	ListRevisionsRequestSchema,
	ListWorldEntriesRequestSchema,
	PreviewWorldConfigRequestSchema,
	ResolveWorldOperationRequestSchema,
	SearchWorldArtifactsRequestSchema,
	UnbindWorldRequestSchema
} from '$lib/proto/carbonpanel/v1/world_pb';
import { createWorldLibraryActions } from '$lib/features/world-library/actions';
import { createWorldLibraryInventory } from '$lib/features/world-library/inventory.svelte';
import type {
	WorldLibraryActionRequests,
	WorldBindingView
} from '$lib/features/world-library/actions';

/**
 * The only production adapter. Views depend on the injected seams; this module
 * is the single place that knows about the Connect client and request schemas.
 */
export const worldLibraryInventory = createWorldLibraryInventory({
	async list(silent) {
		const response = await rpcClient.world.listWorldEntries(
			create(ListWorldEntriesRequestSchema, {}),
			silent ? silentCallOptions : undefined
		);
		return response.entries;
	},
	async detail(id, silent) {
		const response = await rpcClient.world.getWorldEntry(
			create(GetWorldEntryRequestSchema, { worldEntryId: id }),
			silent ? silentCallOptions : undefined
		);
		return response.entry;
	}
});

const requests: WorldLibraryActionRequests = {
	async importWorld(input) {
		const response = await rpcClient.world.importWorld(
			create(ImportWorldRequestSchema, {
				source: input.source,
				url: input.url ?? '',
				uploadId: input.uploadId ?? '',
				displayName: input.displayName,
				sourcePlatform: input.sourcePlatform ?? '',
				licenseAttestation: input.licenseAttestation ?? '',
				sha256Checksum: input.sha256Checksum ?? '',
				requestId: input.requestId
			})
		);
		return { job: response.job, revision: response.revision };
	},
	async checkCompatibility(input) {
		const response = await rpcClient.world.checkCompatibility(
			create(CheckCompatibilityRequestSchema, {
				revisionId: input.revisionId,
				serverId: input.serverId,
				targetMcVersion: input.targetMcVersion
			})
		);
		return response.report;
	},
	async bindWorld(input) {
		return rpcClient.world.bindWorld(
			create(BindWorldRequestSchema, {
				serverId: input.serverId,
				levelName: input.levelName,
				revisionId: input.revisionId,
				window: input.window,
				requestId: input.requestId
			})
		);
	},
	async unbindWorld(input) {
		return rpcClient.world.unbindWorld(
			create(UnbindWorldRequestSchema, {
				serverId: input.serverId,
				levelName: input.levelName,
				requestId: input.requestId
			})
		);
	},
	async resolveWorldOperation(input) {
		return rpcClient.world.resolveWorldOperation(
			create(ResolveWorldOperationRequestSchema, {
				serverId: input.serverId,
				operationId: input.operationId,
				action: input.action,
				note: input.note ?? ''
			})
		);
	},
	async getWorldConfigSchema(input) {
		const response = await rpcClient.world.getWorldConfigSchema(
			create(GetWorldConfigSchemaRequestSchema, {
				edition: input.edition,
				mcVersion: input.mcVersion,
				modLoader: input.modLoader as ModLoader,
				dataVersion: input.dataVersion
			})
		);
		return response.schema;
	},
	async previewWorldConfig(input) {
		const response = await rpcClient.world.previewWorldConfig(
			create(PreviewWorldConfigRequestSchema, {
				edition: input.edition,
				mcVersion: input.mcVersion,
				modLoader: input.modLoader as ModLoader,
				dataVersion: input.dataVersion,
				values: input.values,
				preset: input.preset ?? ''
			})
		);
		return response.preview;
	},
	async createWorldFromConfig(input) {
		const response = await rpcClient.world.createWorldFromConfig(
			create(CreateWorldFromConfigRequestSchema, {
				name: input.name,
				edition: input.edition,
				mcVersion: input.mcVersion,
				modLoader: input.modLoader as ModLoader,
				dataVersion: input.dataVersion,
				values: input.values,
				preset: input.preset ?? '',
				licenseAttestation: input.licenseAttestation ?? '',
				requestId: input.requestId
			})
		);
		return { entry: response.entry, revision: response.revision };
	},
	async searchWorldArtifacts(input) {
		const response = await rpcClient.world.searchWorldArtifacts(
			create(SearchWorldArtifactsRequestSchema, {
				provider: input.provider,
				query: input.query,
				mcVersion: input.mcVersion,
				modLoader: input.modLoader as ModLoader,
				projectType: input.projectType,
				limit: input.limit ?? 20,
				cursor: input.cursor ?? ''
			})
		);
		return {
			artifacts: response.artifacts,
			nextCursor: response.nextCursor,
			rateLimited: response.rateLimited
		};
	},
	async getProviderCapabilities() {
		const response = await rpcClient.world.getProviderCapabilities(
			create(GetProviderCapabilitiesRequestSchema, {})
		);
		return response.providers;
	},
	async listRevisions(entryId) {
		const response = await rpcClient.world.listRevisions(
			create(ListRevisionsRequestSchema, { worldEntryId: entryId })
		);
		return response.revisions;
	},
	async listBindings(input) {
		const response = await rpcClient.world.listBindings(
			create(ListBindingsRequestSchema, {
				serverId: input.serverId ?? '',
				revisionId: input.revisionId ?? ''
			})
		);
		const views: WorldBindingView[] = response.bindings.map((binding) => ({
			id: binding.id,
			serverId: binding.serverId,
			levelName: binding.levelName,
			revisionId: binding.revisionId,
			nodeId: binding.nodeId,
			generation: binding.generation,
			consistency: binding.consistency
		}));
		return views;
	},
	async listImportJobs(entryId) {
		const response = await rpcClient.world.listImportJobs(
			create(ListImportJobsRequestSchema, { worldEntryId: entryId, limit: 50 })
		);
		return response.jobs;
	}
};

export const worldLibraryActions = createWorldLibraryActions(requests, worldLibraryInventory);

export function newWorldRequestId(prefix: string): string {
	return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}
