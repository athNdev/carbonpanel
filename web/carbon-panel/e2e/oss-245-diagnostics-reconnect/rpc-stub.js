// SPDX-License-Identifier: MPL-2.0
// Seeded, disposable RPC control plane for the OSS-245 diagnostics fixture.
// No live credentials, worlds or daemon are involved: every response is
// deferred and resolved explicitly by the browser driver.

const listings = [];
const actions = [];
let failNextClear = false;
let failNextCommand = false;

function deferred() {
	let resolve;
	let reject;
	const promise = new Promise((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

// Mirror the real rpc-client's helpers that components import. The fixture
// replaces the whole module, so a missing export would fail the route import
// before the diagnostics globals are installed.
export function wasRpcErrorToasted(error) {
	return typeof error === 'object' && error !== null && error.__rpcToasted === true;
}

export const silentCallOptions = {
	headers: new Headers({ 'X-Silent-Request': 'true' })
};

export const rpcClient = {
	server: {
		getServerLogs(request) {
			const item = { id: request.id, tail: request.tail, ...deferred() };
			listings.push(item);
			return item.promise;
		},
		clearServerLogs(request) {
			const item = { kind: 'clear', id: request.id, ...deferred() };
			actions.push(item);
			if (failNextClear) {
				failNextClear = false;
				item.reject(new Error('seeded clear failure'));
			}
			return item.promise;
		},
		sendCommand(request) {
			const item = { kind: 'command', id: request.id, command: request.command, ...deferred() };
			actions.push(item);
			if (failNextCommand) {
				failNextCommand = false;
				item.reject(new Error('seeded command failure'));
			}
			return item.promise;
		},
		uploadToMCLogs() {
			return Promise.resolve({ url: '' });
		}
	},
	auth: {
		logout() {
			return Promise.resolve();
		}
	}
};

globalThis.__oss245Rpc = {
	listings,
	actions,
	resolveListing(index, logs) {
		listings[index].resolve({ logs });
	},
	resolveAction(index, value) {
		actions[index].resolve(value);
	},
	failNextClear() {
		failNextClear = true;
	},
	failNextCommand() {
		failNextCommand = true;
	},
	counts() {
		return { listings: listings.length, actions: actions.length };
	}
};
