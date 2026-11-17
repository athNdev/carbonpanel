// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { evaluateSession, isSameOriginProxyPath, resolveEmbedSrc } from './session';

test('only same-origin path prefixes are accepted', () => {
	assert.equal(isSameOriginProxyPath('/api/v1/worldview/map/key/'), true);
	for (const bad of [
		'',
		'https://renderer.example/map',
		'http://host:8080/',
		'//host:8080/',
		'javascript:alert(1)',
		'/api\\evil',
		'/api\nSet-Cookie: x',
		undefined,
		null
	]) {
		assert.equal(isSameOriginProxyPath(bad as string | undefined), false, String(bad));
	}
	assert.equal(resolveEmbedSrc('/map/key/'), '/map/key/');
	assert.equal(resolveEmbedSrc('//evil/map'), null);
});

test('a session verdict only yields a frame when state and path both allow it', () => {
	assert.equal(
		evaluateSession(false, { proxyPath: '/map/key/', state: 7, reason: 'stopped' }).src,
		null
	);
	assert.equal(evaluateSession(true, undefined).src, null);
	assert.equal(
		evaluateSession(true, { proxyPath: 'https://evil/', state: 4, reason: '' }).src,
		null
	);
	assert.equal(
		evaluateSession(true, { proxyPath: '/map/key/', state: 4, reason: '' }).src,
		'/map/key/'
	);
});
