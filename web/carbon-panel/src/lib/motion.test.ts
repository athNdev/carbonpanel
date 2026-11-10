// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { motionDuration, prefersReducedMotion } from './motion';

type Media = { matches: boolean };

function installMotionEnv(opts: { osReduce: boolean; stored: string | null }): () => void {
	const previousDocument = globalThis.document;
	const previousWindow = globalThis.window;
	const media: Media = { matches: opts.osReduce };
	const documentElement = {
		getAttribute: (name: string) => (name === 'data-carbon-motion' ? opts.stored : null)
	};
	(globalThis as { document?: unknown }).document = { documentElement };
	(globalThis as { window?: unknown }).window = {
		matchMedia: (query: string) => {
			assert.equal(query, '(prefers-reduced-motion: reduce)');
			return media;
		}
	};
	return () => {
		if (previousDocument === undefined) {
			Reflect.deleteProperty(globalThis, 'document');
		} else {
			(globalThis as { document?: unknown }).document = previousDocument;
		}
		if (previousWindow === undefined) {
			Reflect.deleteProperty(globalThis, 'window');
		} else {
			(globalThis as { window?: unknown }).window = previousWindow;
		}
	};
}

test('prefersReducedMotion follows OS and stored settings', () => {
	const restoreOs = installMotionEnv({ osReduce: true, stored: null });
	assert.equal(prefersReducedMotion(), true);
	restoreOs();

	const restoreStored = installMotionEnv({ osReduce: false, stored: 'reduced' });
	assert.equal(prefersReducedMotion(), true);
	restoreStored();

	const restoreOff = installMotionEnv({ osReduce: false, stored: 'full' });
	assert.equal(prefersReducedMotion(), false);
	restoreOff();
});

test('motionDuration snaps to zero under reduced motion', () => {
	const restore = installMotionEnv({ osReduce: false, stored: 'reduced' });
	assert.equal(motionDuration(150), 0);
	restore();

	const restoreFull = installMotionEnv({ osReduce: false, stored: 'full' });
	assert.equal(motionDuration(150), 150);
	restoreFull();
});
