import { createWebSocketClient } from '$lib/stores/websocket.svelte';

const client = createWebSocketClient();
client.subscribe('browser-server', 37);
window.socketClient = client;
window.socketTestPage = crypto.randomUUID();
client.connect();
