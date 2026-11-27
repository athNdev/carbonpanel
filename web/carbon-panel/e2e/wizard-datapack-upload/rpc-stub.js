// SPDX-License-Identifier: MPL-2.0
// Seeded, disposable RPC control plane for the create-wizard datapack fixture.
// Every response is local; no live credentials, worlds or daemon are involved.

import { Code, ConnectError } from '@connectrpc/connect';

const calls = [];
let moduleStatus = 1;
let moduleInitialPoll = false;
let failedStart = false;
const libraryPack = {
	id: 'existing-pack',
	indexer: 'modrinth',
	name: 'Indexed Fabric Pack',
	summary: 'An existing, non-favorited library entry',
	mcVersion: '1.21.1',
	latestFileId: 'new-version',
	modLoaders: '["fabric"]',
	recommendedRam: 3072
};
const resourcePackHost = {
	id: 'builtin-resource-pack-host',
	name: 'Resource Pack Host',
	description: 'Serves resource pack ZIPs from a dedicated server folder.',
	defaultEnv: '{"SERVER_ROOT":"/packs"}',
	defaultVolumes:
		'[{"source":"{{server.data_path}}/modules/resource-pack-host/packs","target":"/packs","read_only":true,"create_dir":true}]',
	defaultHooks: []
};
const fixtureBlueprint = {
	id: 'fixture-blueprint',
	name: 'Fixture Java Seed',
	description: 'A qualified Java seed with its own image and memory setting.',
	modLoader: 'forge',
	mcVersion: '1.21.1',
	dockerImage: 'java17',
	defaultEnv: '{"MEMORY":"4G","TYPE":"FORGE"}',
	builtin: false
};

function modVersion(id, fileName, dependencies = []) {
	return {
		id,
		name: fileName,
		versionNumber: '1.0',
		versionType: 'release',
		fileName,
		downloadUrl: `https://cdn.modrinth.com/stub/${fileName}`,
		fileSize: 100n,
		dependencies
	};
}

function record(method, payload) {
	calls.push({ method, payload });
}

const previewOutcomes = [];

// The preview seam in this fixture is the in-process stub itself: no HTTP
// request is made, so there is no HTTP status to report. Record the call and the
// answer the fixture actually gave.
function observePreview(payload, result) {
	return result.then(
		(value) => {
			previewOutcomes.push({
				method: 'previewServerArchive',
				uploadId: payload.uploadId,
				nodeId: payload.nodeId,
				outcome: 'resolved',
				errorCode: null,
				errorName: null,
				findings: value.findings?.length ?? 0,
				destinationIds: value.destinations?.map((destination) => destination.id) ?? [],
				availableBytes: value.availableBytes === undefined ? null : Number(value.availableBytes),
				requiredBytes: value.requiredBytes === undefined ? null : Number(value.requiredBytes)
			});
			return value;
		},
		(error) => {
			previewOutcomes.push({
				method: 'previewServerArchive',
				uploadId: payload.uploadId,
				nodeId: payload.nodeId,
				outcome: 'failed',
				errorCode: typeof error?.code === 'number' ? error.code : null,
				errorName: typeof error?.code === 'number' ? (Code[error.code] ?? null) : null,
				message: error?.rawMessage ?? error?.message ?? null
			});
			throw error;
		}
	);
}

function previewServerArchiveResult(payload) {
	const mode = globalThis.__archivePreviewMode || 'success';
	if (mode === 'fail-once') {
		globalThis.__archivePreviewMode = 'success';
		return Promise.reject(new ConnectError('fixture archive preview refusal', Code.Unavailable));
	}
	if (mode === 'fail') {
		return Promise.reject(new ConnectError('fixture archive preview refusal', Code.Unavailable));
	}
	const findings =
		mode === 'unsupported'
			? [
					{
						code: 5,
						severity: 2,
						message: 'The archive uses a loader that this controller does not support.'
					}
				]
			: [];
	const result = {
		format: globalThis.__archivePreviewFormat === 2 ? 2 : 1,
		schemaVersion: 1,
		exporterVersion: 'fixture-1.0.0',
		mcVersion: '1.20.6',
		modLoader: 3,
		loaderVersion: '0.16.0',
		memory: 3072,
		maxPlayers: 8,
		fileCount: 12,
		expandedBytes: 1048576n,
		requiredBytes: 2097152n,
		availableBytes: payload.nodeId
			? globalThis.__archiveCapacityMode === 'insufficient'
				? 1048576n
				: 4194304n
			: undefined,
		destinations: globalThis.__wizardArchiveDestinations || [
			{ id: 'node-1', name: 'Fixture Node' }
		],
		findings
	};
	if (!payload.nodeId && globalThis.__wizardHoldArchivePreflight) {
		return new Promise((resolve) => {
			globalThis.__wizardReleaseArchivePreflight = () => resolve(result);
		});
	}
	if (payload.nodeId && globalThis.__wizardDelayedArchiveNode === payload.nodeId) {
		const settle = () =>
			globalThis.__wizardDelayedArchiveNodeMode === 'fail'
				? Promise.reject(new ConnectError('stale destination preview refusal', Code.Unavailable))
				: Promise.resolve(result);
		if (globalThis.__wizardHoldArchiveNodePreflight) {
			return new Promise((resolve, reject) => {
				globalThis.__wizardReleaseArchiveNodePreflight = () => {
					delete globalThis.__wizardReleaseArchiveNodePreflight;
					settle().then(resolve, reject);
				};
			});
		}
		return settle();
	}
	return Promise.resolve(result);
}

export const rpcClient = {
	minecraft: {
		getMinecraftVersions() {
			return Promise.resolve({ versions: [{ id: '1.21.1' }], latest: '1.21.1' });
		},
		getModLoaders() {
			return Promise.resolve({
				modloaders: [
					{ name: 'vanilla', displayName: 'Vanilla' },
					{ name: 'fabric', displayName: 'Fabric' },
					{ name: 'modrinth', displayName: 'Modrinth' }
				]
			});
		},
		getDockerImages() {
			return Promise.resolve({ images: [] });
		},
		getLoaderVersions() {
			return Promise.resolve({ versions: [] });
		}
	},
	proxy: {
		getProxyStatus() {
			return Promise.resolve({ enabled: false, baseUrl: '' });
		},
		getProxyListeners() {
			return Promise.resolve({ listeners: [] });
		}
	},
	server: {
		getNextAvailablePort() {
			return Promise.resolve({ port: 25565, usedPorts: [] });
		},
		createServer(payload) {
			record('createServer', payload);
			if (globalThis.__wizardArchiveFailCreateOnce) {
				globalThis.__wizardArchiveFailCreateOnce = false;
				return Promise.reject(new ConnectError('fixture archive create refusal', Code.Aborted));
			}
			const result = { server: { id: 'wizard-server', name: payload.name } };
			if (globalThis.__wizardHoldCreate) {
				return new Promise((resolve) => {
					globalThis.__wizardReleaseCreate = () => resolve(result);
				});
			}
			return Promise.resolve(result);
		},
		exportServerArchive(payload) {
			record('exportServerArchive', payload);
			const tar = payload.format === 2;
			const result = {
				sessionId: tar ? 'export-session-tar' : 'export-session-zip',
				filename: tar ? 'fixture-server.tar.gz' : 'fixture-server.zip',
				totalSize: 1048576n
			};
			const mode = globalThis.__exportMode || 'success';
			if (mode === 'refused') {
				// A ready archive whose scoped download is refused by the fixture
				// download handler (HTTP 403), so the UI must preflight before
				// claiming the download started.
				return Promise.resolve({
					sessionId: 'export-session-refused',
					filename: 'fixture-server-refused.zip',
					totalSize: 1048576n
				});
			}
			if (mode === 'fail') {
				return Promise.reject(
					new ConnectError('fixture export preparation refusal', Code.Internal)
				);
			}
			if (mode === 'hold') {
				return new Promise((resolve) => {
					globalThis.__wizardReleaseExport = () => {
						delete globalThis.__wizardReleaseExport;
						resolve(result);
					};
				});
			}
			if (mode === 'deferred') {
				return new Promise((resolve, reject) => {
					globalThis.__exportPending = globalThis.__exportPending || [];
					globalThis.__exportPending.push({
						format: payload.format,
						sessionId: result.sessionId,
						filename: result.filename,
						resolve: () => resolve(result),
						reject: (message = `deferred export refusal ${payload.format}`) =>
							reject(new ConnectError(message, Code.Internal))
					});
				});
			}
			return Promise.resolve(result);
		},
		previewServerArchive(payload) {
			record('previewServerArchive', payload);
			return observePreview(payload, previewServerArchiveResult(payload));
		},
		listServers() {
			record('listServers', {});
			return Promise.resolve({ servers: [] });
		},
		getServer(payload) {
			record('getServer', payload);
			if (globalThis.__wizardPreparingPolls > 0) {
				globalThis.__wizardPreparingPolls--;
				return Promise.resolve({ server: { id: payload.id, status: 1 } });
			}
			return Promise.resolve({
				server: {
					id: payload.id,
					name: 'Fixture Archive Server',
					description: 'Disposable route fixture',
					containerId: 'fixture-container',
					status:
						globalThis.__fixtureServerRunning || globalThis.location?.search?.includes('running=1')
							? 3
							: failedStart
								? 7
								: 5,
					nodeId: 'node-1',
					port: 25565,
					maxPlayers: 8,
					memory: 3072,
					mcVersion: '1.20.6',
					modLoader: 3,
					loaderVersion: '0.16.0'
				}
			});
		},
		startServer(payload) {
			record('startServer', payload);
			if (
				globalThis.__wizardFailStart &&
				calls.filter((call) => call.method === 'startServer').length === 1
			) {
				failedStart = true;
				return Promise.reject(new ConnectError('Fixture start failed.', Code.Internal));
			}
			failedStart = false;
			return Promise.resolve({ status: 'starting' });
		}
	},
	operation: {
		listOperations(payload) {
			record('listOperations', payload);
			const firstStart = calls.find((call) => call.method === 'startServer');
			return Promise.resolve({
				operations: [
					{
						kind: 'start',
						requestId: firstStart.payload.requestId,
						outcome: 'failed',
						nextAction: globalThis.__wizardJournalLocked ? 'wait' : 'retry_with_new_request_id',
						holdsLock: Boolean(globalThis.__wizardJournalLocked),
						reason: 'Fixture previous start still holds its lock.'
					}
				]
			});
		}
	},
	node: {
		listNodes() {
			if (globalThis.__wizardDenyListNodes) {
				return Promise.reject(
					new ConnectError('insufficient permissions for nodes/read', Code.PermissionDenied)
				);
			}
			return Promise.resolve({
				nodes: globalThis.__wizardNodes || [
					{ id: 'node-1', name: 'Fixture Node', enabled: true, status: 1 }
				]
			});
		}
	},
	blueprint: {
		listBlueprints() {
			return Promise.resolve({ blueprints: [fixtureBlueprint] });
		}
	},
	modpack: {
		searchModpacks(payload) {
			record('searchModpacks', payload);
			const modpacks =
				payload.page === 2
					? [libraryPack]
					: Array.from({ length: 30 }, (_, index) => ({
							...libraryPack,
							id: `library-${index}`,
							name: `Library Pack ${index + 1}`
						}));
			return Promise.resolve({ modpacks, total: 31 });
		},
		listFavorites() {
			return Promise.resolve({ modpacks: [] });
		},
		getModpack() {
			return Promise.resolve({ modpack: null });
		},
		getModpackVersions() {
			return Promise.resolve({
				versions: [
					{ id: 'new-version', displayName: 'Latest Pack File', gameVersions: ['1.21.1'] },
					{
						id: 'old-version',
						displayName: 'Older Pack File',
						gameVersions: ['Forge', '1.20', '1.20.1']
					},
					{ id: 'unknown-version', displayName: 'Metadata Missing', gameVersions: [] }
				]
			});
		},
		getModpackConfig() {
			return Promise.resolve({
				config: { mod_loader: 'modrinth', mc_version: '1.21.1', memory: '3072' }
			});
		}
	},
	modBrowse: {
		searchMods(payload) {
			record('searchMods', payload);
			return Promise.resolve({
				results: [
					{
						id: 'mod-1',
						slug: 'fixture-optimization',
						title: 'Fixture Optimization',
						description: 'A server-side Fabric mod',
						platform: 'modrinth',
						serverSide: 'optional',
						categories: []
					},
					{
						id: 'mod-2',
						slug: 'visuals',
						title: 'Client-only Project',
						description: 'A client-only mod',
						platform: 'modrinth',
						serverSide: 'unsupported',
						categories: []
					}
				]
			});
		},
		getModVersions(payload) {
			record('getModVersions', payload);
			return Promise.resolve({
				versions:
					payload.project === 'fixture-api'
						? [
								modVersion('wrong-version', 'wrong-api.jar'),
								modVersion('required-version', 'required-api.jar')
							]
						: [
								modVersion('root-version', 'fixture-mod.jar', [
									{
										projectId: 'fixture-api',
										versionId: 'required-version',
										dependencyType: 'required',
										title: 'Fixture API'
									}
								])
							]
			});
		},
		installMods(payload) {
			record('installMods', payload);
			const fileName = payload.items[0].fileName;
			if (
				fileName === 'required-api.jar' &&
				calls.filter(
					(call) => call.method === 'installMods' && call.payload.items[0].fileName === fileName
				).length === 1
			) {
				return Promise.resolve({
					success: false,
					installed: [],
					errors: ['Fixture download interrupted.']
				});
			}
			return Promise.resolve({ success: true, installed: [fileName], errors: [] });
		}
	},
	module: {
		listModuleTemplates(payload) {
			record('listModuleTemplates', payload);
			return Promise.resolve({ templates: [resourcePackHost] });
		},
		createModule(payload) {
			record('createModule', payload);
			moduleStatus = globalThis.__wizardModuleMode ? 5 : 1;
			moduleInitialPoll = globalThis.__wizardModuleMode === 'error';
			if (globalThis.__wizardModuleMode === 'lost') {
				return Promise.reject(
					new ConnectError('Fixture module response lost.', Code.DeadlineExceeded)
				);
			}
			return Promise.resolve({ module: { id: 'fixture-module', status: 1 } });
		},
		listModules(payload) {
			record('listModules', payload);
			return Promise.resolve({
				modules: [
					{
						id: 'fixture-module',
						status: moduleStatus,
						serverId: payload.serverId,
						templateId: resourcePackHost.id
					}
				]
			});
		},
		getModule(payload) {
			const status = moduleInitialPoll ? 6 : moduleStatus;
			moduleInitialPoll = false;
			record('getModule', { ...payload, status });
			return Promise.resolve({
				module: {
					id: payload.id,
					status,
					containerId: status === 1 ? 'fixture-sidecar-container' : ''
				}
			});
		},
		recreateModule(payload) {
			record('recreateModule', payload);
			moduleStatus = 1;
			return Promise.resolve({ status: 'recreated' });
		}
	},
	datapack: {
		searchDatapacks() {
			return Promise.resolve({ datapacks: [], totalCount: 0, pageSize: 20, offset: 0 });
		},
		installDatapack(payload) {
			record('installDatapack', payload);
			return Promise.resolve({});
		},
		uploadDatapack(payload) {
			record('uploadDatapack', payload);
			return Promise.resolve({});
		}
	}
};

export const silentCallOptions = {};

// ServerArchiveExport suppresses a duplicate toast when the shared RPC client
// already toasted the failure; this fixture never pre-toasts.
export function wasRpcErrorToasted() {
	return false;
}

globalThis.__wizardCalls = calls;
globalThis.__wizardPreviewOutcomes = previewOutcomes;
globalThis.__wizardReset = () => {
	calls.length = 0;
	previewOutcomes.length = 0;
};
