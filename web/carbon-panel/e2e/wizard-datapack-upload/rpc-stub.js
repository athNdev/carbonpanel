// SPDX-License-Identifier: MPL-2.0
// Seeded, disposable RPC control plane for the create-wizard datapack fixture.
// Every response is local; no live credentials, worlds or daemon are involved.

const calls = [];

function record(method, payload) {
	calls.push({ method, payload });
}

export const rpcClient = {
	minecraft: {
		getMinecraftVersions() {
			return Promise.resolve({ versions: [{ id: '1.21.1' }], latest: '1.21.1' });
		},
		getModLoaders() {
			return Promise.resolve({ modloaders: [] });
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
			return Promise.resolve({ server: { id: 'wizard-server', name: payload.name } });
		}
	},
	node: {
		listNodes() {
			return Promise.resolve({ nodes: [{ id: 'node-1', enabled: true, status: 1 }] });
		}
	},
	blueprint: {
		listBlueprints() {
			return Promise.resolve({ blueprints: [] });
		}
	},
	modpack: {
		listFavorites() {
			return Promise.resolve({ modpacks: [] });
		},
		getModpack() {
			return Promise.resolve({ modpack: null });
		},
		getModpackVersions() {
			return Promise.resolve({ versions: [] });
		},
		getModpackConfig() {
			return Promise.resolve({});
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

globalThis.__wizardCalls = calls;
globalThis.__wizardReset = () => {
	calls.length = 0;
};
