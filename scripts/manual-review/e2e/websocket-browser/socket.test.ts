// SPDX-License-Identifier: MPL-2.0
// Bun supplies this module at test time; the frontend does not include Bun types.
// @ts-expect-error Bun's test module is provided by the runner
import { test, expect, mock } from 'bun:test';
import { create, fromBinary, toBinary } from '../../../../web/carbon-panel/node_modules/@bufbuild/protobuf';
import {
	WSMessageType,
	WebSocketClientMessageSchema,
	WebSocketServerMessageSchema,
	AuthFailMessageSchema
} from '../../../../web/carbon-panel/src/lib/proto/carbonpanel/v1/websocket_pb';

let logoutCount = 0;
mock.module('$app/env', () => ({ browser: false }));
mock.module('$lib/stores/auth', () => ({
	authStore: {
		subscribe: (fn: (v: object) => void) => {
			fn({ token: '' });
			return () => {};
		},
		logout: () => {
			logoutCount++;
			return Promise.resolve();
		}
	}
}));
// Bun does not compile Svelte runes; browser smoke exercises the compiled state.
(globalThis as unknown as { $state: <T>(v: T) => T }).$state = (v) => v;
const { createWebSocketClient } = await import('../../../../web/carbon-panel/src/lib/stores/websocket.svelte');

test('stale sockets and timers cannot change a replacement and tail survives reconnect', () => {
	const sockets: FakeSocket[] = [];
	const timers = new Map<number, () => void>();
	let next = 1;
	const dependencies = {
		enabled: () => true,
		url: () => 'ws://example.test/ws',
		token: () => 'token',
		setSessionCookie: () => {},
		createSocket: () => {
			const socket = new FakeSocket();
			sockets.push(socket);
			return socket as unknown as WebSocket;
		},
		setTimeout: ((fn: () => void) => {
			const id = next++;
			timers.set(id, fn);
			return id;
		}) as unknown as typeof setTimeout,
		clearTimeout: ((id: number) => timers.delete(id)) as unknown as typeof clearTimeout,
		setInterval: ((fn: () => void) => {
			const id = next++;
			timers.set(id, fn);
			return id;
		}) as unknown as typeof setInterval,
		clearInterval: ((id: number) => timers.delete(id)) as unknown as typeof clearInterval
	};
	const client = createWebSocketClient(dependencies);
	client.connect();
	const a = sockets[0];
	a.open();
	a.auth();
	client.subscribe('server', 37);
	client.reconnect();
	const b = sockets[1];
	b.open();
	b.auth();
	const subscription = b.sent
		.map((bytes) => fromBinary(WebSocketClientMessageSchema, bytes))
		.find((msg) => msg.payload.case === 'subscribe');
	expect(subscription?.payload.case === 'subscribe' && subscription.payload.value.tail).toBe(37);
	let messages = 0;
	client.onMessage(() => messages++);
	a.open();
	a.error();
	a.auth();
	a.closed(1006);
	expect(client.state.connectionState).toBe('authenticated');
	expect(client.state.error).toBeNull();
	expect(b.closedCount).toBe(0);
	expect(messages).toBe(0);
	b.closed(1006);
	b.closed(1006);
	const queued = [...timers.values()];
	expect(queued.length).toBe(1);
	client.disconnect();
	queued.forEach((callback) => callback());
	expect(sockets.length).toBe(2);
	expect(client.state.connectionState).toBe('disconnected');
});

test('AUTH_FAIL is terminal: socket dropped, session cleared, no reconnect spinner', () => {
	const sockets: FakeSocket[] = [];
	const timers = new Map<number, () => void>();
	let next = 1;
	const dependencies = {
		enabled: () => true,
		url: () => 'ws://example.test/ws',
		token: () => 'token',
		setSessionCookie: () => {},
		createSocket: () => {
			const socket = new FakeSocket();
			sockets.push(socket);
			return socket as unknown as WebSocket;
		},
		setTimeout: ((fn: () => void) => {
			const id = next++;
			timers.set(id, fn);
			return id;
		}) as unknown as typeof setTimeout,
		clearTimeout: ((id: number) => timers.delete(id)) as unknown as typeof clearTimeout,
		setInterval: ((fn: () => void) => {
			const id = next++;
			timers.set(id, fn);
			return id;
		}) as unknown as typeof setInterval,
		clearInterval: ((id: number) => timers.delete(id)) as unknown as typeof clearInterval
	};
	const client = createWebSocketClient(dependencies);
	client.connect();
	const socket = sockets[0];
	socket.open();
	socket.auth();
	expect(client.state.connectionState).toBe('authenticated');
	const logoutsBefore = logoutCount;

	socket.authFail();

	// Fail closed: a distinct signed-out state, the socket closed, the session
	// cleared, and no queued retry that could spin or resurrect the connection.
	expect(client.state.connectionState).toBe('signed-out');
	expect(client.state.error).toBe('Authentication failed');
	expect(socket.closedCount).toBe(1);
	expect(logoutCount).toBe(logoutsBefore + 1);
	expect(timers.size).toBe(0);
	expect(client.isReady).toBe(false);

	// Late events from the rejected generation cannot undo the signed-out state.
	socket.open();
	socket.auth();
	socket.closed(1006);
	expect(client.state.connectionState).toBe('signed-out');
	expect(sockets.length).toBe(1);
	expect(timers.size).toBe(0);
});

class FakeSocket {
	readyState = 0;
	binaryType = '';
	onopen: (() => void) | null = null;
	onclose: ((event: { code: number; reason: string }) => void) | null = null;
	onerror: ((event: object) => void) | null = null;
	onmessage: ((event: { data: ArrayBuffer }) => void) | null = null;
	sent: Uint8Array[] = [];
	closedCount = 0;
	send(data: Uint8Array) {
		this.sent.push(data);
	}
	close() {
		this.closedCount++;
		this.readyState = 3;
	}
	open() {
		this.readyState = 1;
		this.onopen?.();
	}
	closed(code: number) {
		this.readyState = 3;
		this.onclose?.({ code, reason: '' });
	}
	error() {
		this.onerror?.({});
	}
	auth() {
		const data = toBinary(
			WebSocketServerMessageSchema,
			create(WebSocketServerMessageSchema, { type: WSMessageType.WS_MESSAGE_TYPE_AUTH_OK })
		);
		this.onmessage?.({ data: data.slice().buffer as ArrayBuffer });
	}
	authFail() {
		const data = toBinary(
			WebSocketServerMessageSchema,
			create(WebSocketServerMessageSchema, {
				type: WSMessageType.WS_MESSAGE_TYPE_AUTH_FAIL,
				payload: {
					case: 'authFail',
					value: create(AuthFailMessageSchema, { error: 'invalid token' })
				}
			})
		);
		this.onmessage?.({ data: data.slice().buffer as ArrayBuffer });
	}
}
