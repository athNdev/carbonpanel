// SPDX-License-Identifier: MPL-2.0
// Auth_FAIL triggers the same logout path as an RPC 401; navigation is inert
// inside the fixture so the signed-out state stays visible for assertions.
export function goto() {
	return Promise.resolve();
}
