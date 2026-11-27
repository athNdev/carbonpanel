// SPDX-License-Identifier: MPL-2.0
// Test-only reactive connection state. The console's fallback polling reacts to
// the same state changes the real WebSocket store emits, without opening a
// socket or depending on the app bundle.
export const wsState = $state({
	connectionState: 'disconnected',
	error: null
});
