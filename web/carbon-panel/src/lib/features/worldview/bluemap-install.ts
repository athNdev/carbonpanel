// SPDX-License-Identifier: MPL-2.0
/**
 * OSS-388: the in-context "Install BlueMap" action.
 *
 * New installs go through the EXISTING ModuleService create/start RPCs, so the
 * normal modules/create + modules/start authorization and operation journal
 * apply. There is no parallel unguarded path and no public renderer URL.
 *
 * Adoption is read-only: if the exact server already has a builtin-bluemap
 * module, nothing is created, started, reconfigured or restarted; the existing
 * module's config is authoritative. Private discovery then observes it.
 */

import { create } from '@bufbuild/protobuf';
import { Code, ConnectError, type CallOptions } from '@connectrpc/connect';
import {
	CreateModuleRequestSchema,
	ListModulesRequestSchema,
	type CreateModuleRequest,
	type CreateModuleResponse,
	type ListModulesRequest,
	type ListModulesResponse,
	type Module
} from '$lib/proto/carbonpanel/v1/module_pb';
import type { ListNodesRequest, ListNodesResponse, Node } from '$lib/proto/carbonpanel/v1/node_pb';
import type { WorldViewTarget } from '$lib/proto/carbonpanel/v1/worldview_pb';

export const BLUEMAP_TEMPLATE_ID = 'builtin-bluemap';

/** Consent is an explicit operator action, never a default. The actor and
 * time are added by the backend, not supplied by the browser. */
export interface BlueMapConsent {
	accepted: boolean;
	reuseConfiguration?: boolean;
}

export function retainedBlueMapSetup(error: unknown) {
	if (!(error instanceof ConnectError) || error.code !== Code.FailedPrecondition) return null;
	switch (error.metadata.get('Carbonpanel-Bluemap-Setup')) {
		case 'retained-config':
			return 'retained-config';
		case 'config-missing':
			return 'config-missing';
		case 'download-consent':
			return 'download-consent';
		case 'world-config':
			return 'world-config';
		default:
			return null;
	}
}

export interface BlueMapInstallRpc {
	listModules(request: ListModulesRequest): Promise<{ modules: Module[] }>;
	createModule(request: CreateModuleRequest): Promise<{ module: Module }>;
	listNodes(request: ListNodesRequest): Promise<{ nodes: Node[] }>;
}

/** The grouped `rpcClient` exposes module/node calls as `rpcClient.module.*`
 * and `rpcClient.node.*`, not as flat methods. Adapt explicitly: casting the
 * grouped client to BlueMapInstallRpc leaves the methods undefined at runtime
 * and makes the in-view install fail before any RPC is sent (OSS-388). */
export interface GroupedBlueMapRpc {
	module: {
		listModules(request: ListModulesRequest, options?: CallOptions): Promise<ListModulesResponse>;
		createModule(
			request: CreateModuleRequest,
			options?: CallOptions
		): Promise<CreateModuleResponse>;
	};
	node: {
		listNodes(request: ListNodesRequest, options?: CallOptions): Promise<ListNodesResponse>;
	};
}

export function adaptBlueMapRpc(
	grouped: GroupedBlueMapRpc,
	options?: CallOptions
): BlueMapInstallRpc {
	return {
		listModules: (request) => grouped.module.listModules(request, options),
		createModule: async (request) => {
			const response = await grouped.module.createModule(request, options);
			if (!response.module) throw new Error('CreateModule returned no module');
			return { module: response.module };
		},
		listNodes: (request) => grouped.node.listNodes(request, options)
	};
}

export type InstallEligibility = { eligible: boolean; reason: string };

/**
 * A renderer can only serve a map when its module lives on a local node we can
 * reach over the owned network, and the caller holds the existing module
 * permissions. Remote nodes must use an explicit operator endpoint instead.
 */
export function blueMapInstallEligibility(
	can: (resource: string, action: string) => boolean,
	nodes: Node[],
	target: WorldViewTarget | undefined
): InstallEligibility {
	if (!target) return { eligible: false, reason: 'The world target is still loading.' };
	if (!can('modules', 'create') || !can('modules', 'start')) {
		return {
			eligible: false,
			reason: 'Installing a renderer needs modules/create and modules/start permissions.'
		};
	}
	if (!target.worldRef?.levelName) {
		return {
			eligible: false,
			reason: "This world's actual level name is unknown; BlueMap cannot be configured safely."
		};
	}
	const node = nodes.find((n) => n.id === target.nodeId);
	if (!node?.isLocal) {
		return {
			eligible: false,
			reason:
				'Private renderer discovery is only available on the local node. On a remote node, configure an explicit node-local world_view endpoint instead.'
		};
	}
	return { eligible: true, reason: '' };
}

export type InstallOutcome = 'installed' | 'adopted' | 'reused';

export interface InstallResult {
	outcome: InstallOutcome;
	module: Module;
}

/**
 * Idempotent by exact server/template identity. An existing module is adopted
 * without any mutation; a new module is created with the managed signal and
 * explicit consent. The BACKEND derives the private port, read-only level bind,
 * pinned image and per-world config (including accept-download) from the real
 * world; no client-side init command or URL is trusted.
 */
export async function installBlueMap(
	rpc: BlueMapInstallRpc,
	serverId: string,
	consent: BlueMapConsent
): Promise<InstallResult> {
	if (!consent.accepted) {
		throw new Error('Explicit acceptance of the download and license terms is required.');
	}
	const existing = await rpc.listModules(
		create(ListModulesRequestSchema, { serverId, templateId: BLUEMAP_TEMPLATE_ID })
	);
	const adopted = existing.modules.find(
		(m) => m && m.serverId === serverId && m.templateId === BLUEMAP_TEMPLATE_ID
	);
	if (adopted) {
		// Never overwrite config, reconfigure, restart or duplicate. The
		// existing container and its config remain authoritative.
		return { outcome: 'adopted', module: adopted };
	}

	const created = await rpc.createModule(
		create(CreateModuleRequestSchema, {
			name: 'BlueMap',
			serverId,
			templateId: BLUEMAP_TEMPLATE_ID,
			startImmediately: true,
			// No access URLs: the renderer origin must never reach a browser.
			accessUrls: [],
			metadata: {
				// Only the explicit acceptance signal comes from the operator
				// action. The actor and time are derived server-side by
				// ModuleService from the authenticated principal and clock.
				bluemap_managed: 'true',
				bluemap_consent_accepted: 'true',
				...(consent.reuseConfiguration && { bluemap_reuse_config: 'true' }),
				bluemap_version: 'v5.29'
			}
		})
	);
	return { outcome: consent.reuseConfiguration ? 'reused' : 'installed', module: created.module };
}
