#!/usr/bin/env node
// SPDX-License-Identifier: MPL-2.0
// Fixture coverage for the Connect JSON int64 wire values consumed by the
// real World Library swap qualification harness.
import assert from "node:assert/strict";
import { normalizeWireInt } from "./qualify-world-swap.mjs";

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

console.log("PASS: Connect JSON int64 wire normalization for the world swap harness");
