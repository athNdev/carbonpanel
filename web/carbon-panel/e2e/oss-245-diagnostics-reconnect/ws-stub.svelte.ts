// SPDX-License-Identifier: MPL-2.0
// Seeded WebSocket control plane. This is a `.svelte.ts` module so the real
// ServerConsole sees a genuinely reactive `state`, matching the production
// websocket singleton shape without opening a socket.

type LogHandler = (serverId: string, logs: Array<{ message: string; level: number }>) => void;

const subscriptions: Array<{ id: string; tail: number }> = [];
const logHandlers: LogHandler[] = [];
const logEntryHandlers: LogHandler[] = [];
const commandResultHandlers: Array<(result: unknown) => void> = [];

let connectCalls = 0;
let reconnectCalls = 0;

const state = $state<{ connectionState: string; error: string | null }>({
	connectionState: 'authenticated',
	error: null
});

export const wsClient = {
	state,
	connect() {
		connectCalls++;
	},
	reconnect() {
		reconnectCalls++;
	},
	subscribe(id: string, tail: number) {
		subscriptions.push({ id, tail });
	},
	unsubscribe(id: string) {
		for (let i = subscriptions.length - 1; i >= 0; i--) {
			if (subscriptions[i].id === id) subscriptions.splice(i, 1);
		}
	},
	onLogs(handler: LogHandler) {
		logHandlers.push(handler);
		return () => {
			const index = logHandlers.indexOf(handler);
			if (index >= 0) logHandlers.splice(index, 1);
		};
	},
	onLogEntry(handler: LogHandler) {
		logEntryHandlers.push(handler);
		return () => {
			const index = logEntryHandlers.indexOf(handler);
			if (index >= 0) logEntryHandlers.splice(index, 1);
		};
	},
	onCommandResult(handler: (result: unknown) => void) {
		commandResultHandlers.push(handler);
		return () => {
			const index = commandResultHandlers.indexOf(handler);
			if (index >= 0) commandResultHandlers.splice(index, 1);
		};
	},
	sendCommand() {},
	get isReady() {
		return this.state.connectionState === 'authenticated';
	}
};

globalThis.__oss245Ws = {
	emitLogs(serverId: string, messages: string[]) {
		const logs = messages.map((message) => ({ message, level: 2 }));
		logHandlers.forEach((handler) => handler(serverId, logs));
	},
	emitLogEntry(serverId: string, messages: string[]) {
		const logs = messages.map((message) => ({ message, level: 2 }));
		logEntryHandlers.forEach((handler) => handler(serverId, logs));
	},
	emitCommandResult(result: unknown) {
		commandResultHandlers.forEach((handler) => handler(result));
	},
	authFail() {
		// Mirror the production fail-closed transition driven by an AUTH_FAIL
		// frame: terminal signed-out state, no reconnect loop.
		wsClient.state.connectionState = 'signed-out';
		wsClient.state.error = 'Authentication failed';
	},
	state() {
		return {
			connectionState: wsClient.state.connectionState,
			error: wsClient.state.error,
			connectCalls,
			reconnectCalls,
			subscriptions: subscriptions.map((item) => ({ ...item })),
			logHandlers: logHandlers.length,
			logEntryHandlers: logEntryHandlers.length
		};
	}
};
