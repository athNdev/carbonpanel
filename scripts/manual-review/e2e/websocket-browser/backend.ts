// SPDX-License-Identifier: MPL-2.0
import { create, fromBinary, toBinary } from '../../../../web/carbon-panel/node_modules/@bufbuild/protobuf';
import { WebSocketClientMessageSchema, WebSocketServerMessageSchema, WSMessageType } from '../../../../web/carbon-panel/src/lib/proto/carbonpanel/v1/websocket_pb';

let firstSocket: { close: (code: number, reason: string) => void } | undefined;
const backend = Bun.serve({
  hostname: '127.0.0.1', port: 0,
  fetch(request, server) {
    if (new URL(request.url).pathname === '/close') {
      firstSocket?.close(1011, 'test reconnect');
      return new Response('closed');
    }
    return server.upgrade(request) ? undefined : new Response('not found', { status: 404 });
  },
  websocket: {
    open(socket) { firstSocket ??= socket; },
    message(socket, data) {
      const message = fromBinary(WebSocketClientMessageSchema, new Uint8Array(data as ArrayBuffer));
      if (message.type === WSMessageType.WS_MESSAGE_TYPE_AUTH) {
        socket.send(toBinary(WebSocketServerMessageSchema, create(WebSocketServerMessageSchema, { type: WSMessageType.WS_MESSAGE_TYPE_AUTH_OK })));
      } else if (message.payload.case === 'subscribe') {
        console.log(JSON.stringify({ tail: message.payload.value.tail }));
      }
    }
  }
});
console.log(JSON.stringify({ port: backend.port }));
