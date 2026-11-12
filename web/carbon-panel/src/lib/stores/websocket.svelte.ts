// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

import { browser } from '$app/environment';
import { get } from 'svelte/store';
import { create, toBinary, fromBinary } from '@bufbuild/protobuf';
import type { LogEntry } from '$lib/proto/carbonpanel/v1/server_pb';
import {
	WSMessageType,
	WebSocketClientMessageSchema,
	WebSocketServerMessageSchema,
	AuthMessageSchema,
	SubscribeMessageSchema,
	UnsubscribeMessageSchema,
	CommandMessageSchema,
	type WebSocketServerMessage,
	type LogsMessage,
	type LogMessage,
	type CommandResultMessage
} from '$lib/proto/carbonpanel/v1/websocket_pb';
import { authStore } from '$lib/stores/auth';

/* eslint-disable svelte/prefer-svelte-reactivity -- Private queues and handler registries are not UI state. */

export type ConnectionState = 'disconnected' | 'connecting' | 'connected' | 'authenticated';

export interface WebSocketState {
	connectionState: ConnectionState;
	error: string | null;
}

type MessageHandler = (message: WebSocketServerMessage) => void;
type LogHandler = (serverId: string, logs: LogEntry[]) => void;
type LogEntryHandler = (serverId: string, logs: LogEntry[]) => void;
type CommandResultHandler = (result: CommandResultMessage) => void;

export interface WebSocketDependencies {
	enabled: () => boolean;
	url: () => string;
	token: () => string;
	setSessionCookie: (token: string) => void;
	createSocket: (url: string) => WebSocket;
	setTimeout: typeof setTimeout;
	clearTimeout: typeof clearTimeout;
	setInterval: typeof setInterval;
	clearInterval: typeof clearInterval;
}

const defaultDependencies: WebSocketDependencies = {
	enabled: () => browser,
	url: () =>
		`${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/ws`,
	token: () => get(authStore).token || '',
	setSessionCookie: (token) => {
		document.cookie = `cp_session=${encodeURIComponent(token)}; Path=/; SameSite=Strict`;
	},
	createSocket: (url) => new WebSocket(url),
	setTimeout: ((...args: Parameters<typeof setTimeout>) =>
		globalThis.setTimeout(...args)) as typeof setTimeout,
	clearTimeout: (timer) => globalThis.clearTimeout(timer),
	setInterval: ((...args: Parameters<typeof setInterval>) =>
		globalThis.setInterval(...args)) as typeof setInterval,
	clearInterval: (timer) => globalThis.clearInterval(timer)
};

export class WebSocketClient {
	constructor(private readonly deps: WebSocketDependencies = defaultDependencies) {}
	private socket: WebSocket | null = null;
	private generation = 0;
	private reconnectAttempts = 0;
	private maxReconnectAttempts = 5;
	private reconnectDelay = 1000;
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private pingTimer: ReturnType<typeof setInterval> | null = null;

	// Log batching
	private static readonly LOG_FLUSH_INTERVAL_MS = 100;
	private logBuffer = new Map<string, LogEntry[]>();
	private logFlushTimer: ReturnType<typeof setInterval> | null = null;

	// Svelte 5 runes for reactive state
	state = $state<WebSocketState>({
		connectionState: 'disconnected',
		error: null
	});

	// Event handlers
	private messageHandlers = new Set<MessageHandler>();
	private logHandlers = new Set<LogHandler>();
	private logEntryHandlers = new Set<LogEntryHandler>();
	private commandResultHandlers = new Set<CommandResultHandler>();

	// Preserve each subscription's requested history across reconnects.
	private subscriptions = new Map<string, number>();

	connect(isRetry = false): void {
		if (!this.deps.enabled()) return;
		if (this.socket?.readyState === 1) return;
		if (this.state.connectionState === 'connecting') return;
		if (this.reconnectTimer) {
			this.deps.clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
		const generation = ++this.generation;

		// A fresh, user-initiated connect resets the backoff budget so a manual
		// retry (or a page interaction) is not permanently blocked by the five
		// attempts consumed during an earlier outage.
		if (!isRetry) {
			this.reconnectAttempts = 0;
		}

		this.state.connectionState = 'connecting';
		this.state.error = null;

		// Pre-upgrade credential (MINE-164) rides the cp_session cookie so the
		// session token is not written into the URL (MINE-185).
		const preAuth = this.deps.token();
		if (preAuth) {
			this.deps.setSessionCookie(preAuth);
		}
		const wsUrl = this.deps.url();

		try {
			const socket = this.deps.createSocket(wsUrl);
			this.socket = socket;
			socket.binaryType = 'arraybuffer';
			const current = () => this.generation === generation && this.socket === socket;

			socket.onopen = () => {
				if (!current()) return;
				console.log('[WS] Connected');
				this.state.connectionState = 'connected';
				this.reconnectAttempts = 0;

				// Authenticate with token or empty string for non auth to get anon token
				this.authenticate(this.deps.token());

				// Start ping timer and log flush timer
				this.startPingTimer();
				this.startLogFlushTimer();
			};

			socket.onclose = (event) => {
				if (!current()) return;
				this.socket = null;
				console.log('[WS] Disconnected:', event.code, event.reason);
				this.cleanup();
				if (generation !== this.generation) return;
				this.state.connectionState = 'disconnected';

				// Attempt reconnection if not a clean close
				if (event.code !== 1000) {
					this.scheduleReconnect(generation);
				}
			};

			socket.onerror = (error) => {
				if (!current()) return;
				console.error('[WS] Error:', error);
				this.state.error = 'WebSocket connection error';
				// Some failures surface as onerror without a following onclose;
				// close explicitly so the reconnect path always runs.
				if (socket.readyState !== 3) {
					socket.close();
				}
			};

			socket.onmessage = (event) => {
				if (!current()) return;
				this.handleMessage(event.data, generation);
			};
		} catch (error) {
			console.error('[WS] Failed to connect:', error);
			this.state.connectionState = 'disconnected';
			this.state.error = 'Failed to establish connection';
			this.scheduleReconnect(generation);
		}
	}

	disconnect(): void {
		++this.generation;
		const socket = this.socket;
		this.socket = null;
		if (socket) socket.close(1000, 'Client disconnect');
		this.cleanup();
		this.state.connectionState = 'disconnected';
		this.subscriptions.clear();
	}

	reconnect(): void {
		++this.generation;
		this.cleanup();
		if (this.socket) {
			try {
				this.socket.close();
			} catch {
				// Socket may already be in closed/closing state; ignore close errors during cleanup
			}
			this.socket = null;
		}
		this.state.connectionState = 'disconnected';
		this.state.error = null;
		this.reconnectAttempts = 0;
		this.connect(false);
	}

	private cleanup(): void {
		if (this.pingTimer) {
			this.deps.clearInterval(this.pingTimer);
			this.pingTimer = null;
		}
		if (this.reconnectTimer) {
			this.deps.clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
		if (this.logFlushTimer) {
			this.deps.clearInterval(this.logFlushTimer);
			this.logFlushTimer = null;
		}
		this.flushLogBuffer();
	}

	private startLogFlushTimer(): void {
		if (this.logFlushTimer) return;
		const generation = this.generation;
		this.logFlushTimer = this.deps.setInterval(() => {
			if (generation !== this.generation) return;
			this.flushLogBuffer();
		}, WebSocketClient.LOG_FLUSH_INTERVAL_MS);
	}

	private flushLogBuffer(): void {
		if (this.logBuffer.size === 0) return;

		for (const [serverId, logs] of this.logBuffer) {
			if (logs.length > 0) {
				// Isolate each subscriber. Previously a single throwing handler aborted
				// the forEach (silently starving every later subscriber) and escaped the
				// interval callback as an uncaught exception, which also skipped the
				// logBuffer.clear() below and made it re-throw on every tick. Because
				// no throw escapes now, the clear is always reached.
				this.logEntryHandlers.forEach((handler) => {
					try {
						handler(serverId, logs);
					} catch (error) {
						console.error('[WS] log entry handler threw; isolating subscriber', error);
					}
				});
			}
		}
		this.logBuffer.clear();
	}

	private scheduleReconnect(generation: number): void {
		if (generation !== this.generation || this.reconnectTimer) return;
		const isTerminalBackoff = this.reconnectAttempts >= this.maxReconnectAttempts;
		if (isTerminalBackoff) {
			console.log('[WS] Max rapid reconnect attempts reached; continuing periodic retries');
			this.state.connectionState = 'disconnected';
			this.state.error = 'Connection lost. Retrying in background or click Reconnect.';
		}

		const delay = isTerminalBackoff
			? 30000
			: Math.min(30000, this.reconnectDelay * Math.pow(2, this.reconnectAttempts));

		this.reconnectAttempts++;

		console.log(`[WS] Reconnecting in ${delay}ms (attempt ${this.reconnectAttempts})`);
		this.reconnectTimer = this.deps.setTimeout(() => {
			if (generation !== this.generation) return;
			this.reconnectTimer = null;
			this.connect(true);
		}, delay);
	}

	private startPingTimer(): void {
		// Send ping every 30 seconds
		const generation = this.generation;
		this.pingTimer = this.deps.setInterval(() => {
			if (generation !== this.generation) return;
			this.sendPing();
		}, 30000);
	}

	private handleMessage(data: ArrayBuffer, generation: number): void {
		try {
			const msg = fromBinary(WebSocketServerMessageSchema, new Uint8Array(data));

			// Notify all message handlers
			this.messageHandlers.forEach((handler) => handler(msg));
			if (generation !== this.generation) return;

			switch (msg.type) {
				case WSMessageType.WS_MESSAGE_TYPE_AUTH_OK:
					console.log('[WS] Authenticated');
					this.state.connectionState = 'authenticated';
					// Re-subscribe to all active subscriptions
					this.resubscribeAll();
					break;

				case WSMessageType.WS_MESSAGE_TYPE_AUTH_FAIL:
					console.error('[WS] Auth failed:', msg.payload.value);
					this.state.error = 'Authentication failed';
					break;

				case WSMessageType.WS_MESSAGE_TYPE_SUBSCRIBED:
					if (msg.payload.case === 'subscribed') {
						console.log('[WS] Subscribed to:', msg.payload.value.serverId);
					}
					break;

				case WSMessageType.WS_MESSAGE_TYPE_UNSUBSCRIBED:
					if (msg.payload.case === 'unsubscribed') {
						console.log('[WS] Unsubscribed from:', msg.payload.value.serverId);
					}
					break;

				case WSMessageType.WS_MESSAGE_TYPE_LOGS:
					if (msg.payload.case === 'logs') {
						const logsMsg = msg.payload.value as LogsMessage;
						this.logHandlers.forEach((handler) => handler(logsMsg.serverId, logsMsg.logs));
					}
					break;

				case WSMessageType.WS_MESSAGE_TYPE_LOG:
					if (msg.payload.case === 'log') {
						const logMsg = msg.payload.value as LogMessage;
						if (logMsg.log) {
							// Buffer log entries for batched dispatch
							const buffer = this.logBuffer.get(logMsg.serverId) || [];
							buffer.push(logMsg.log);
							this.logBuffer.set(logMsg.serverId, buffer);
						}
					}
					break;

				case WSMessageType.WS_MESSAGE_TYPE_COMMAND_RESULT:
					if (msg.payload.case === 'commandResult') {
						const result = msg.payload.value as CommandResultMessage;
						this.commandResultHandlers.forEach((handler) => handler(result));
					}
					break;

				case WSMessageType.WS_MESSAGE_TYPE_ERROR:
					if (msg.payload.case === 'error') {
						console.error('[WS] Server error:', msg.payload.value.error);
						this.state.error = msg.payload.value.error;
					}
					break;

				case WSMessageType.WS_MESSAGE_TYPE_PONG:
					// Pong received, connection is alive
					break;
			}
		} catch (error) {
			console.error('[WS] Failed to parse message:', error);
		}
	}

	private send(data: Uint8Array): boolean {
		if (this.socket?.readyState !== 1) {
			console.warn('[WS] Cannot send, socket not open');
			return false;
		}
		this.socket.send(data);
		return true;
	}

	private authenticate(token: string): void {
		const msg = create(WebSocketClientMessageSchema, {
			type: WSMessageType.WS_MESSAGE_TYPE_AUTH,
			payload: {
				case: 'auth',
				value: create(AuthMessageSchema, { token })
			}
		});
		this.send(toBinary(WebSocketClientMessageSchema, msg));
	}

	subscribe(serverId: string, tail: number = 500): void {
		this.subscriptions.set(serverId, tail);

		if (this.state.connectionState !== 'authenticated') {
			return;
		}

		const msg = create(WebSocketClientMessageSchema, {
			type: WSMessageType.WS_MESSAGE_TYPE_SUBSCRIBE,
			payload: {
				case: 'subscribe',
				value: create(SubscribeMessageSchema, { serverId, tail })
			}
		});
		this.send(toBinary(WebSocketClientMessageSchema, msg));
	}

	unsubscribe(serverId: string): void {
		this.subscriptions.delete(serverId);

		if (this.state.connectionState !== 'authenticated') {
			return;
		}

		const msg = create(WebSocketClientMessageSchema, {
			type: WSMessageType.WS_MESSAGE_TYPE_UNSUBSCRIBE,
			payload: {
				case: 'unsubscribe',
				value: create(UnsubscribeMessageSchema, { serverId })
			}
		});
		this.send(toBinary(WebSocketClientMessageSchema, msg));
	}

	sendCommand(serverId: string, command: string): void {
		if (this.state.connectionState !== 'authenticated') {
			return;
		}

		const msg = create(WebSocketClientMessageSchema, {
			type: WSMessageType.WS_MESSAGE_TYPE_COMMAND,
			payload: {
				case: 'command',
				value: create(CommandMessageSchema, { serverId, command })
			}
		});
		this.send(toBinary(WebSocketClientMessageSchema, msg));
	}

	private sendPing(): void {
		if (
			this.state.connectionState !== 'authenticated' &&
			this.state.connectionState !== 'connected'
		) {
			return;
		}

		const msg = create(WebSocketClientMessageSchema, {
			type: WSMessageType.WS_MESSAGE_TYPE_PING,
			payload: { case: undefined, value: undefined }
		});
		this.send(toBinary(WebSocketClientMessageSchema, msg));
	}

	private resubscribeAll(): void {
		for (const [serverId, tail] of this.subscriptions) {
			const msg = create(WebSocketClientMessageSchema, {
				type: WSMessageType.WS_MESSAGE_TYPE_SUBSCRIBE,
				payload: {
					case: 'subscribe',
					value: create(SubscribeMessageSchema, { serverId, tail })
				}
			});
			this.send(toBinary(WebSocketClientMessageSchema, msg));
		}
	}

	// Event handler registration
	onMessage(handler: MessageHandler): () => void {
		this.messageHandlers.add(handler);
		return () => this.messageHandlers.delete(handler);
	}

	onLogs(handler: LogHandler): () => void {
		this.logHandlers.add(handler);
		return () => this.logHandlers.delete(handler);
	}

	onLogEntry(handler: LogEntryHandler): () => void {
		this.logEntryHandlers.add(handler);
		return () => this.logEntryHandlers.delete(handler);
	}

	onCommandResult(handler: CommandResultHandler): () => void {
		this.commandResultHandlers.add(handler);
		return () => this.commandResultHandlers.delete(handler);
	}

	// Check if connected and authenticated
	get isReady(): boolean {
		return this.state.connectionState === 'authenticated';
	}
}

export function createWebSocketClient(
	deps: WebSocketDependencies = defaultDependencies
): WebSocketClient {
	return new WebSocketClient(deps);
}

export const wsClient = createWebSocketClient();
