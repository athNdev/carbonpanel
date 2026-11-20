// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { setTimeout as sleep } from 'node:timers/promises';
import mineflayer from 'mineflayer';

const username = 'ReviewGuest';
const host = '127.0.0.1';
const panelPort = Number(process.env.REVIEW_PANEL_PORT);
const gamePort = Number(process.env.REVIEW_FIXTURE_GAME_PORT);
const serverId = process.env.REVIEW_FIXTURE_SERVER_ID;
const token = process.env.REVIEW_FIXTURE_TOKEN;
const validPort = (port) => Number.isInteger(port) && port > 0 && port <= 65535;

if (!validPort(panelPort) || !validPort(gamePort) || !serverId || !token) {
  console.error('Missing or invalid review fixture inputs');
  process.exit(1);
}

const deadline = AbortSignal.timeout(55_000);
const disconnected = new AbortController();
const active = AbortSignal.any([deadline, disconnected.signal]);
let bot;
let ended = false;
let observed = false;

try {
  bot = mineflayer.createBot({
    host,
    port: gamePort,
    username,
    auth: 'offline',
    version: '1.21.1',
    hideErrors: true,
    logErrors: false,
  });
  bot.on('error', () => disconnected.abort());
  bot.once('kicked', () => disconnected.abort());
  bot.once('end', () => {
    ended = true;
    disconnected.abort();
  });

  await new Promise((resolve, reject) => {
    active.addEventListener('abort', reject, { once: true });
    bot.once('spawn', () => {
      active.removeEventListener('abort', reject);
      resolve();
    });
  });

  while (!observed) {
    const response = await fetch(
      `http://${host}:${panelPort}/carbonpanel.v1.PlayerService/ListPlayers`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
        body: JSON.stringify({ serverId, query: username, limit: 20 }),
        redirect: 'error',
        signal: active,
      },
    );
    if (!response.ok) throw new Error('Roster request failed');

    const { players = [] } = await response.json();
    active.throwIfAborted();
    observed = players.some(
      (player) =>
        player.serverId === serverId &&
        player.ref?.name === username &&
        Boolean(player.ref.identityKey) &&
        (player.presence === 'PLAYER_PRESENCE_ONLINE' || player.presence === 1),
    );
    if (!observed) await sleep(1000, undefined, { signal: active });
  }
} catch {
  console.error('The disposable panel did not observe ReviewGuest online on the seeded server');
} finally {
  if (bot && !ended) {
    const end = new Promise((resolve) => bot.once('end', resolve));
    try {
      bot.quit();
      await Promise.race([end, sleep(2000, undefined, { ref: false })]);
    } catch {
      console.error('The review client could not close cleanly');
    }
  }
}

if (observed && ended) {
  console.log('The disposable panel observed ReviewGuest online on the seeded server');
  process.exit(0);
}
process.exit(1);
