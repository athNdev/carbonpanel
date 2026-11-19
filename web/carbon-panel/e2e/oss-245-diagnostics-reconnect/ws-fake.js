// SPDX-License-Identifier: MPL-2.0
// Seeded disposable WebSocket transport. The production websocket client and
// its generation ownership stay under test; only the browser socket is faked.
import { create, fromBinary, toBinary } from '@bufbuild/protobuf';
import {
	WSMessageType,
	WebSocketClientMessageSchema,
	WebSocketServerMessageSchema,
	AuthFailMessageSchema,
	LogsMessageSchema,
	LogMessageSchema
} from '$lib/proto/carbonpanel/v1/websocket_pb';
import { LogEntrySchema } from '$lib/proto/carbonpanel/v1/server_pb';
import { wsClient } from '$lib/stores/websocket.svelte';

const instances = [];

class FakeWebSocket {
	constructor(url) {
		this.url = url;
		this.readyState = 0;
		this.binaryType = '';
		this.sent = [];
		this.closeCalls = [];
		this.onopen = null;
		this.onclose = null;
		this.onerror = null;
		this.onmessage = null;
		instances.push(this);
	}
	send(data) {
		this.sent.push(fromBinary(WebSocketClientMessageSchema, new Uint8Array(data)));
	}
	close(code, reason) {
		this.closeCalls.push({ code, reason });
		this.readyState = 3;
	}
	open() {
		this.readyState = 1;
		this.onopen?.();
		// Reply to the AUTH frame the real client sends on open.
		this.frame(
			create(WebSocketServerMessageSchema, {
				type: WSMessageType.WS_MESSAGE_TYPE_AUTH_OK
			})
		);
	}
	frame(message) {
		this.onmessage?.({ data: toBinary(WebSocketServerMessageSchema, message).slice().buffer });
	}
}

// Replace the browser socket before the console mounts and calls connect().
globalThis.WebSocket = FakeWebSocket;

function logEntries(messages) {
	return messages.map((message) => create(LogEntrySchema, { message, level: 'info' }));
}

globalThis.__oss245Ws = {
	open() {
		instances[instances.length - 1]?.open();
	},
	emitLogs(serverId, messages) {
		for (const socket of instances) {
			socket.frame(
				create(WebSocketServerMessageSchema, {
					type: WSMessageType.WS_MESSAGE_TYPE_LOGS,
					payload: {
						case: 'logs',
						value: create(LogsMessageSchema, { serverId, logs: logEntries(messages) })
					}
				})
			);
		}
	},
	emitLogEntry(serverId, messages) {
		for (const message of messages) {
			for (const socket of instances) {
				socket.frame(
					create(WebSocketServerMessageSchema, {
						type: WSMessageType.WS_MESSAGE_TYPE_LOG,
						payload: {
							case: 'log',
							value: create(LogMessageSchema, {
								serverId,
								log: create(LogEntrySchema, { message, level: 'info' })
							})
						}
					})
				);
			}
		}
	},
	authFail() {
		for (const socket of instances) {
			socket.frame(
				create(WebSocketServerMessageSchema, {
					type: WSMessageType.WS_MESSAGE_TYPE_AUTH_FAIL,
					payload: {
						case: 'authFail',
						value: create(AuthFailMessageSchema, { error: 'invalid token' })
					}
				})
			);
		}
	},
	state() {
		const current = instances[instances.length - 1];
		const sent = current?.sent ?? [];
		return {
			connectionState: wsClient.state.connectionState,
			error: wsClient.state.error,
			socketCount: instances.length,
			closeCalls: current ? [...current.closeCalls] : [],
			subscribeCount: sent.filter(
				(message) => message.type === WSMessageType.WS_MESSAGE_TYPE_SUBSCRIBE
			).length,
			unsubscribeCount: sent.filter(
				(message) => message.type === WSMessageType.WS_MESSAGE_TYPE_UNSUBSCRIBE
			).length
		};
	}
};
