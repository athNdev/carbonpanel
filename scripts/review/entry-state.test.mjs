// SPDX-License-Identifier: MPL-2.0
import test from 'node:test';
import assert from 'node:assert/strict';
import { waitForEntryState } from './entry-state.mjs';

// A deterministic delayed-hydration fake. `count()` always reports an empty
// DOM (the SPA has not mounted yet), while `waitFor` models the moment each
// mutually exclusive semantic form actually becomes visible. This reproduces
// CI run 44290007929: devstack had already seeded the admin, so the login form
// appears after hydration; an immediate count misreads it as bootstrap.
function delayedHydrationPage(visibleAt, defaultTimeout = 250) {
  return {
    locator(selector) {
      const loc = {
        count() {
          return 0;
        },
        first() {
          return loc;
        },
        waitFor({ timeout } = {}) {
          const at = visibleAt[selector];
          if (at === undefined) {
            return new Promise((_, reject) =>
              setTimeout(() => reject(new Error(`timeout waiting for ${selector}`)), timeout ?? defaultTimeout),
            );
          }
          return new Promise((resolve) => setTimeout(resolve, at));
        },
      };
      return loc;
    },
  };
}

const SELECTORS = {
  login: 'input[placeholder="Enter your username"]',
  bootstrap: 'input[placeholder="Choose admin username"]',
  authenticated: 'a[href^="/servers/"]',
  fatal: '[data-testid="app-fatal"]',
};

test('delayed hydration: a seeded login form wins over an immediate-count bootstrap guess', async () => {
  const page = delayedHydrationPage({ [SELECTORS.login]: 30 });
  const { state, selector } = await waitForEntryState(page, SELECTORS, 200);
  assert.equal(state, 'login');
  assert.equal(selector, SELECTORS.login);
});

test('delayed hydration: bootstrap form is detected when it is the state that appears', async () => {
  const page = delayedHydrationPage({ [SELECTORS.bootstrap]: 30 });
  const { state } = await waitForEntryState(page, SELECTORS, 200);
  assert.equal(state, 'bootstrap');
});

test('delayed hydration: an already-authenticated app is detected', async () => {
  const page = delayedHydrationPage({ [SELECTORS.authenticated]: 20 });
  const { state } = await waitForEntryState(page, SELECTORS, 200);
  assert.equal(state, 'authenticated');
});

test('delayed hydration: a fatal state is detected and surfaced', async () => {
  const page = delayedHydrationPage({ [SELECTORS.fatal]: 10 });
  const { state } = await waitForEntryState(page, SELECTORS, 200);
  assert.equal(state, 'fatal');
});

test('fail closed: no reachable semantic state rejects instead of guessing bootstrap', async () => {
  const page = delayedHydrationPage({}, 40);
  await assert.rejects(() => waitForEntryState(page, SELECTORS, 40), /no entry state/i);
});

test('config guards: empty or invalid selectors reject before waiting', async () => {
  const page = delayedHydrationPage({});
  await assert.rejects(() => waitForEntryState(page, {}, 50), /no entry-state selectors/i);
  await assert.rejects(
    () => waitForEntryState(page, { login: '' }, 50),
    /invalid selector for entry state login/i,
  );
});
