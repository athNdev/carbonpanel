// SPDX-License-Identifier: MPL-2.0
import { readFileSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const read = (path) => readFileSync(join(root, path), 'utf8').replace(/\r\n/g, '\n');
const policy = JSON.parse(read('toolchain-versions.json'));
for (const tool of ['go', 'bun', 'buf']) {
	if (typeof policy[tool] !== 'string' || !/^\d+\.\d+\.\d+$/.test(policy[tool]))
		throw new Error(`Invalid ${tool} version`);
}
function expect(path, pattern, value) {
	const found = read(path).match(pattern)?.[1];
	if (found !== value) throw new Error(`${path}: expected ${value}, got ${found || 'missing'}`);
}
expect('go.mod', /^go\s+([^\s]+)\s*$/m, policy.go);
for (const name of ['carbon-panel', 'geyser', 'status']) {
	expect(`docker/Dockerfile.${name}`, /^ARG GO_IMAGE=golang:([^\s]+)-alpine$/m, policy.go);
}
expect('docker/Dockerfile.carbon-panel', /^ARG BUN_IMAGE=oven\/bun:([^\s]+)-alpine$/m, policy.bun);
expect('docker/Dockerfile.carbon-panel', /^ARG BUF_IMAGE=bufbuild\/buf:([^\s]+)$/m, policy.buf);
expect('Makefile', /^BUF_IMAGE \?= bufbuild\/buf:([^\s]+)$/m, policy.buf);
expect(
	'scripts/proto-generate.mjs',
	/const expected = process\.env\.BUF_VERSION \|\| '([^']+)'/,
	policy.buf
);
let bunActions = 0;
let bufActions = 0;
for (const name of readdirSync(join(root, '.github/workflows')).filter((name) =>
	/\.ya?ml$/.test(name)
)) {
	const path = `.github/workflows/${name}`;
	const lines = read(path).split('\n');
	for (let index = 0; index < lines.length; index++) {
		if (lines[index].includes('oven-sh/setup-bun@')) {
			bunActions++;
			const found = lines
				.slice(index + 1, index + 5)
				.join('\n')
				.match(/bun-version:\s*['"]?([^'"\s]+)/)?.[1];
			if (found !== policy.bun)
				throw new Error(
					`${path}: Bun action is ${found || 'missing a version'}, expected ${policy.bun}`
				);
		}
		if (lines[index].includes('bufbuild/buf-setup-action@')) {
			bufActions++;
			const found = lines
				.slice(index + 1, index + 5)
				.join('\n')
				.match(/version:\s*['"]?([^'"\s]+)/)?.[1];
			if (found !== policy.buf)
				throw new Error(
					`${path}: Buf action is ${found || 'missing a version'}, expected ${policy.buf}`
				);
		}
		for (const match of lines[index].matchAll(/bufbuild\/buf:([^\s'"}]+)/g)) {
			if (match[1] !== policy.buf)
				throw new Error(`${path}: Buf image is ${match[1]}, expected ${policy.buf}`);
		}
	}
}
if (!bunActions || !bufActions) throw new Error('Missing expected CI tool setup actions');
console.log(`Toolchain policy matches Go ${policy.go}, Bun ${policy.bun} and Buf ${policy.buf}.`);
