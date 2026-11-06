// SPDX-License-Identifier: MPL-2.0
// Finalises the Inter (UI) + JetBrains Mono (code) trial: the design tokens
// must name the vendored faces, and every @font-face reference in app.css
// must resolve to a real self-hosted woff2 file.
import test from 'node:test';
import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';

const tokens = readFileSync(new URL('./tokens.css', import.meta.url), 'utf8');
const appCss = readFileSync(new URL('../../app.css', import.meta.url), 'utf8');

function tokenValue(name: string): string {
	const match = tokens.match(new RegExp(`${name}:\\s*([^;]+);`));
	assert.ok(match, `Missing token ${name}`);
	return match[1];
}

test('font tokens name the vendored Inter and JetBrains Mono faces', () => {
	const sans = tokenValue('--carbon-font-sans');
	const mono = tokenValue('--carbon-font-mono');
	assert.match(sans, /Inter/, 'sans stack must name Inter');
	assert.match(mono, /JetBrains Mono/, 'mono stack must name JetBrains Mono');
	assert.ok(!tokens.includes('Plex'), 'tokens must not reference Plex anymore');
});

test('app.css self-hosts the declared faces, no font CDN or npm imports', () => {
	assert.ok(!appCss.includes('@fontsource'), 'app.css must not import @fontsource');
	const faces = Array.from(
		appCss.matchAll(
			/@font-face\s*\{[^}]*font-family:\s*'([^']+)'[^}]*src:\s*url\('([^']+)'\)[^}]*\}/g
		)
	);
	const families = faces.map((m) => m[1]);
	assert.ok(families.includes('Inter Variable'), 'expected an Inter @font-face');
	assert.ok(families.includes('JetBrains Mono Variable'), 'expected a JetBrains Mono @font-face');
	assert.ok(faces.length > 0, 'expected at least one @font-face block');
	for (const [, family, url] of faces) {
		assert.ok(
			url.startsWith('/fonts/'),
			`${family} must be served from the app origin, got ${url}`
		);
		const onDisk = new URL(`../../../static/${url.replace('/fonts/', 'fonts/')}`, import.meta.url);
		assert.ok(
			existsSync(onDisk),
			`${family} references ${url} which must exist under static/fonts`
		);
		const bytes = readFileSync(onDisk);
		assert.strictEqual(
			bytes.subarray(0, 4).toString('ascii'),
			'wOF2',
			`${url} must be a genuine woff2 file`
		);
	}
});
