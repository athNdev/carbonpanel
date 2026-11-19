// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Semantic entry-state detection for the review journey.
//
// The login route hydrates asynchronously: `page.goto(..., domcontentloaded)`
// resolves before the Svelte app has mounted its form. Deciding what to do from
// an immediate `locator(...).count()` misreads a still-hydrating, already-seeded
// login page as first-user bootstrap. Instead we wait for ONE of the mutually
// exclusive semantic states to become visible, and fail closed if none does.

export const ENTRY_STATES = Object.freeze(['login', 'bootstrap', 'authenticated', 'fatal']);

/**
 * Wait for exactly one mutually exclusive entry state to become visible.
 *
 * `selectors` maps a state name to a Playwright selector for a DOM node that
 * is present only in that state. The states must be mutually exclusive; the
 * first one that becomes visible wins. No fixed sleep and no reliance on an
 * pre-hydration `count()`: the signal itself is the ready condition.
 *
 * @returns {Promise<{state: string, selector: string}>}
 */
export async function waitForEntryState(page, selectors, timeoutMs = 20000) {
  const entries = Object.entries(selectors);
  if (entries.length === 0) {
    throw new Error('no entry-state selectors configured');
  }
  for (const [state, selector] of entries) {
    if (!ENTRY_STATES.includes(state) || typeof selector !== 'string' || !selector) {
      throw new Error(`invalid selector for entry state ${state}`);
    }
  }

  // Every attempt waits on the same semantic ready signal. A losing attempt
  // rejects on timeout; attach a no-op catch so it cannot become an unhandled
  // rejection while the winner is already being used.
  const attempts = entries.map(async ([state, selector]) => {
    await page.locator(selector).first().waitFor({ state: 'visible', timeout: timeoutMs });
    return { state, selector };
  });
  for (const attempt of attempts) attempt.catch(() => {});

  try {
    return await Promise.any(attempts);
  } catch {
    // Never guess bootstrap when no state became visible: that is exactly the
    // hydration race this replaces. Fail closed with a diagnostic instead.
    throw new Error(
      `no entry state became visible within ${timeoutMs}ms (${entries.map(([s]) => s).join(', ')})`,
    );
  }
}
