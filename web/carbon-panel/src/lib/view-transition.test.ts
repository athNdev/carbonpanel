// SPDX-License-Identifier: MPL-2.0
// Regression for rapid client navigation with motion enabled: skipTransition()
// rejects both ViewTransition promises with an AbortError. The old layout used
// `void next.finished.finally(...)`, which propagated that rejection as an
// unhandled pageerror ("Transition was skipped").
import test from 'node:test';
import assert from 'node:assert/strict';
import { isViewTransitionAbort, settleViewTransition } from './view-transition';

type Recorder = {
	transition: ViewTransition;
	readyCatch: (error: unknown) => unknown;
	finishedCatch: (error: unknown) => unknown;
	finishedFinally: () => void;
};

function recordingTransition(): Recorder {
	let readyCatch: (error: unknown) => unknown = () => {
		throw new Error('no ready catch handler installed');
	};
	let finishedCatch: (error: unknown) => unknown = () => {
		throw new Error('no finished catch handler installed');
	};
	let finishedFinally: () => void = () => {
		throw new Error('no finished finally handler installed');
	};
	const ready = {
		catch(handler: (error: unknown) => unknown) {
			readyCatch = handler;
			return this;
		}
	};
	const finished = {
		catch(handler: (error: unknown) => unknown) {
			finishedCatch = handler;
			return this;
		},
		finally(handler: () => void) {
			finishedFinally = handler;
			return this;
		}
	};
	return {
		transition: { ready, finished } as unknown as ViewTransition,
		get readyCatch() {
			return readyCatch;
		},
		get finishedCatch() {
			return finishedCatch;
		},
		get finishedFinally() {
			return finishedFinally;
		}
	};
}

const abortError = () => Object.assign(new Error('Transition was skipped'), { name: 'AbortError' });

test('a skipped transition installs handlers, ignores abort and clears once', () => {
	const recorder = recordingTransition();
	let clears = 0;
	settleViewTransition(recorder.transition, () => clears++);

	// Both promises must have a rejection handler; the old code had none for
	// `ready` and none for the abort rejection of `finished`.
	assert.doesNotThrow(() => recorder.readyCatch(abortError()));
	assert.doesNotThrow(() => recorder.finishedCatch(abortError()));
	recorder.finishedFinally();
	assert.equal(clears, 1, 'cleanup must run exactly once');
});

test('unrelated transition failures are rethrown, not swallowed', () => {
	const recorder = recordingTransition();
	settleViewTransition(recorder.transition, () => {});
	const real = new Error('real transition failure');
	assert.throws(() => recorder.readyCatch(real), /real transition failure/);
	assert.throws(() => recorder.finishedCatch(real), /real transition failure/);
});

test('only AbortError is classified as an expected skip', () => {
	assert.equal(isViewTransitionAbort(abortError()), true);
	assert.equal(isViewTransitionAbort(new Error('Transition was skipped')), false);
	assert.equal(isViewTransitionAbort(new DOMException('other', 'SyntaxError')), false);
});
