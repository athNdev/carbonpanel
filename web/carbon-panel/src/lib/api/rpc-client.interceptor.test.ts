// SPDX-License-Identifier: MPL-2.0
// Direct contract coverage for the RPC interceptor's single-toast behaviour:
// one toast per distinct message inside the 3s dedupe window, the thrown error
// carries the `__rpcToasted` tag, and silent calls neither toast nor tag.
import test, { afterEach } from 'node:test';
import assert from 'node:assert/strict';
// @ts-expect-error -- Bun supplies this test-only module; the app tsconfig omits Bun types.
import { jest, mock } from 'bun:test';
// Load the same Headers implementation the DOM tests use before the RPC module
// creates `silentCallOptions`; mixing Bun and Happy DOM Headers would drop the
// silent header when a later test drives the real transport.
import '../../../test/dom-setup.mjs';

const errorToasts: string[] = [];
mock.module('svelte-sonner', () => ({
	toast: {
		error: (message: unknown) => errorToasts.push(String(message)),
		success: () => {}
	}
}));
mock.module('$app/navigation', () => ({
	goto: () => Promise.resolve(),
	invalidateAll: () => Promise.resolve(),
	onNavigate: () => {},
	beforeNavigate: () => {},
	afterNavigate: () => {},
	pushState: () => {},
	replaceState: () => {},
	preloadData: () => Promise.resolve()
}));
mock.module('$app/paths', () => ({
	resolve: (path: string) => path,
	asset: (path: string) => path
}));
mock.module('$app/env', () => ({ browser: false }));
mock.module('$app/state', () => ({
	page: { url: new URL('http://localhost/'), params: {}, data: {} }
}));

const { Code, ConnectError } = await import('@connectrpc/connect');
const { authInterceptor, silentCallOptions, wasRpcErrorToasted } = await import('./rpc-client');

afterEach(() => {
	jest.useRealTimers();
	errorToasts.length = 0;
});

type FakeRequest = {
	header: Headers;
	service: { typeName: string };
	method: { name: string };
};

function fakeRequest(options: { silent?: boolean } = {}): FakeRequest {
	return {
		header: new Headers(options.silent ? silentCallOptions.headers : undefined),
		service: { typeName: 'carbonpanel.v1.TestService' },
		method: { name: 'Probe' }
	};
}

async function failedCall(message: string, options: { silent?: boolean } = {}): Promise<unknown> {
	const next = async (): Promise<never> => {
		throw new ConnectError(message, Code.FailedPrecondition);
	};
	const invokeInterceptor = authInterceptor(next as never) as (
		request: FakeRequest
	) => Promise<unknown>;
	try {
		await invokeInterceptor(fakeRequest(options));
	} catch (error) {
		return error;
	}
	assert.fail('the interceptor must reject a failed call');
}

test('a failed call toasts once and tags the thrown error', async () => {
	const message = 'interceptor: actionable failure';
	const error = await failedCall(message);

	assert.deepEqual(errorToasts, [message]);
	assert.equal(wasRpcErrorToasted(error), true);
});

test('an identical failure inside the 3s window adds no toast but stays tagged', async () => {
	jest.useFakeTimers();
	const message = 'interceptor: deduped failure';

	const first = await failedCall(message);
	const second = await failedCall(message);
	assert.deepEqual(errorToasts, [message], 'the second failure must not toast again');
	assert.equal(wasRpcErrorToasted(first), true);
	assert.equal(wasRpcErrorToasted(second), true, 'deduped failures still carry the tag');

	jest.advanceTimersByTime(2999);
	await failedCall(message);
	assert.deepEqual(errorToasts, [message], 'the message stays deduped before 3s');

	jest.advanceTimersByTime(1);
	await failedCall(message);
	assert.deepEqual(errorToasts, [message, message], 'the message may toast again after 3s');
});

test('silentCallOptions suppress the interceptor toast and leave the error untagged', async () => {
	const message = 'interceptor: silent failure';
	const error = await failedCall(message, { silent: true });

	assert.deepEqual(errorToasts, []);
	assert.equal(wasRpcErrorToasted(error), false);
	assert.equal(silentCallOptions.headers.get('X-Silent-Request'), 'true');
});

test('an unmarked error is not reported as toasted', () => {
	assert.equal(wasRpcErrorToasted(new Error('plain failure')), false);
	assert.equal(wasRpcErrorToasted({ __rpcToasted: false }), false);
	assert.equal(wasRpcErrorToasted(undefined), false);
	assert.equal(wasRpcErrorToasted(null), false);
});
