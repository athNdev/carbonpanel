// SPDX-License-Identifier: MPL-2.0
// Seeded, disposable RPC control plane for the OSS-245 diagnostics fixture.
// No live credentials, worlds or daemon are involved: every response is
// deferred and resolved explicitly by the browser driver.

const listings = [];
const actions = [];
let failNextClear = false;

function deferred() {
	let resolve;
	let reject;
	const promise = new Promise((res, rej) => {
		resolve = res;
		reject = rej;
	});
	return { promise, resolve, reject };
}

export const rpcClient = {
	server: {
		getServerLogs(request) {
			const item = { id: request.id, tail: request.tail, ...deferred() };
			listings.push(item);
			globalThis.__oss245Notify?.();
			return item.promise;
		},
		clearServerLogs(request) {
			const item = { kind: 'clear', id: request.id, ...deferred() };
			actions.push(item);
			globalThis.__oss245Notify?.();
			if (failNextClear) {
				failNextClear = false;
				item.reject(new Error('seeded clear failure'));
			}
			return item.promise;
		},
		sendCommand(request) {
			const item = { kind: 'command', id: request.id, command: request.command, ...deferred() };
			actions.push(item);
			globalThis.__oss245Notify?.();
			return item.promise;
		},
		uploadToMCLogs() {
			return Promise.resolve({ url: '' });
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
	counts() {
		return { listings: listings.length, actions: actions.length };
	}
};
