// SPDX-License-Identifier: MPL-2.0
// Inert $app/state page stub for the disposable normal-route fixture. The
// detail entry renders the real /servers/[id] route with a fixture id; the
// wizard entry renders the real /servers/new route with no route params.
const url = new URL(globalThis.location?.href ?? 'http://localhost/');
const detail = url.pathname.includes('detail');
export const page = {
	url,
	params: detail ? { id: 'fixture-server' } : {},
	data: {},
	state: {},
	shallow: { url }
};
