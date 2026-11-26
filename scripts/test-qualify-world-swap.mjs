#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
// Fixture coverage for the Connect JSON int64 wire values consumed by the
// real World Library swap qualification harness.
import assert from "node:assert/strict";
import { normalizeWireInt, isInterruptedTransport, connectError } from "./qualify-world-swap.mjs";
import { safeToRetry, transportCause } from "./qualify-compose-game.mjs";

// Exact shape observed from a successful Go Connect JSON GetUploadStatus
// response: int64 fields are decimal strings, not JSON numbers.
const wire = JSON.parse(`{
  "sessionId": "upload-session-fixture",
  "bytesReceived": "1048576",
  "totalBytes": "1048576",
  "chunksReceived": 1,
  "totalChunks": 1,
  "missingChunks": [],
  "completed": true,
  "tempPath": "/tmp/upload-session-fixture"
}`);

const payload = Buffer.alloc(1048576);
assert.equal(normalizeWireInt(wire.bytesReceived, "bytesReceived"), BigInt(payload.length));
assert.equal(normalizeWireInt(wire.totalBytes, "totalBytes"), BigInt(payload.length));

// A JSON number is still accepted (older proxies / test doubles).
assert.equal(normalizeWireInt(42, "bytesReceived"), 42n);
assert.equal(normalizeWireInt(42n, "bytesReceived"), 42n);
assert.equal(normalizeWireInt("0", "bytesReceived"), 0n);

// Non-integral or non-numeric values must fail loudly, not silently coerce.
assert.throws(() => normalizeWireInt("12.5", "bytesReceived"), /unexpected int64 wire value/);
assert.throws(() => normalizeWireInt("not-a-number", "bytesReceived"), /unexpected int64 wire value/);
assert.throws(() => normalizeWireInt(null, "bytesReceived"), /unexpected int64 wire value/);

// The observed Undici socket closure is classified from error.cause.code,
// not from the top-level "fetch failed" message.
const undiciSocket = new TypeError("fetch failed");
undiciSocket.cause = { code: "UND_ERR_SOCKET", message: "other side closed" };
assert.match(transportCause(undiciSocket).text, /UND_ERR_SOCKET/);
assert.equal(isInterruptedTransport(undiciSocket), true);

// A deterministic product/precondition failure must never be replayed.
const precondition = new Error("WorldService/BindWorld: 400 failed_precondition");
assert.equal(isInterruptedTransport(precondition), false);

// Both idempotency-key spellings used by the harness make a mutation replayable.
assert.equal(safeToRetry("ServerService", "CreateServer", { requestId: "create-1" }), true);
assert.equal(safeToRetry("ServerService", "CreateServer", { request_id: "create-2" }), true);
assert.equal(safeToRetry("WorldService", "BindWorld", { request_id: "bind-1" }), true);
assert.equal(safeToRetry("ServerService", "GetServer", {}), true);
assert.equal(safeToRetry("BackupService", "CreateBackup", {}), false);

// Only the structured Connect conflict with the real ErrBindRequestConflict
// message qualifies as the intended wrong-revision replay refusal.
const conflict = connectError(new Error(
  'WorldService/BindWorld: 400 {"code":"failed_precondition","message":"world bind request ID conflicts with the requested binding"}'));
assert.equal(conflict.code, "failed_precondition");
assert.equal(conflict.status, 400);
assert.match(conflict.text, /world bind request ID conflicts with the requested binding/);
const unrelated = connectError(new Error('WorldService/BindWorld: 429 {"code":"resource_exhausted","message":"already busy"}'));
assert.notEqual(unrelated.code, "failed_precondition");

console.log("PASS: wire normalization, retry boundaries and structured replay refusal fixtures");
