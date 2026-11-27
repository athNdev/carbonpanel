// SPDX-License-Identifier: MPL-2.0
// Offline fail-closed tests for the hosted production-UI acceptance driver.
// They never launch a browser, start Docker or contact a controller.
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import { test } from "node:test";
import path from "node:path";

import {
  LOGIN_SELECTORS,
  PLAN_SCHEMA,
  RECORD_SCHEMA,
  artifactPath,
  assertLoopbackBase,
  formatFromFilename,
  formatLabel,
  parsePlan,
  planArtifactPath,
  previewFacts,
  readHead,
  recordCreatedImport,
  safeArtifactName,
  serverIdFromUrl,
  upsertImportEntry,
  writeDriverRecord,
} from "./qualify-archive-ui-hosted.mjs";

const HEAD = "a".repeat(40);
const source = { serverId: "srv-source-1" };

function refusalPlan(overrides = {}) {
  return {
    schema: PLAN_SCHEMA,
    phase: "refusal",
    head: HEAD,
    base: "http://127.0.0.1:18080",
    source,
    ...overrides,
  };
}

function exportPlan(overrides = {}) {
  return {
    schema: PLAN_SCHEMA,
    phase: "export",
    head: HEAD,
    base: "http://127.0.0.1:18080",
    source,
    exports: [
      { format: "zip", expectedLabel: "ZIP (.zip)" },
      { format: "tar.gz", expectedLabel: "TAR.GZ (.tar.gz)" },
    ],
    ...overrides,
  };
}

function importPlan(overrides = {}) {
  return {
    schema: PLAN_SCHEMA,
    phase: "import",
    head: HEAD,
    base: "http://127.0.0.1:18080",
    source,
    remoteNode: { id: "node-remote", name: "archive-rehearsal-remote" },
    imports: [
      {
        format: "zip",
        file: "source.zip",
        expectedLabel: "ZIP (.zip)",
        name: "archive-ui-zip",
        port: 25586,
      },
      {
        format: "tar.gz",
        file: "source.tar.gz",
        expectedLabel: "TAR.GZ (.tar.gz)",
        name: "archive-ui-tar-gz",
        port: 25587,
      },
    ],
    ...overrides,
  };
}

test("loopback controller bases are accepted and everything else is refused", () => {
  assert.equal(assertLoopbackBase("http://127.0.0.1:18080"), "http://127.0.0.1:18080");
  assert.equal(assertLoopbackBase("http://localhost:1"), "http://localhost:1");
  for (const bad of [
    "https://127.0.0.1:18080",
    "http://192.0.2.10:18080",
    "http://panel.example.com",
    "http://user:pass@127.0.0.1:18080",
    "http://127.0.0.1:18080/servers",
    "not a url",
    "",
  ]) {
    assert.throws(() => assertLoopbackBase(bad), /base URL|loopback|credentials|bare origin|plain http/);
  }
});

test("download and screenshot names cannot escape the evidence directory", () => {
  assert.equal(safeArtifactName("../../etc/passwd", "fallback"), "passwd");
  assert.equal(safeArtifactName("server archive.zip", "fallback"), "server_archive.zip");
  assert.equal(safeArtifactName("", "fallback"), "fallback");
  assert.equal(safeArtifactName("..", "fallback"), "fallback");
  assert.throws(() => safeArtifactName("", ""), /fallback/);

  const out = path.resolve("tmp", "evidence", "ui");
  assert.equal(artifactPath(out, "a.zip"), path.join(out, "a.zip"));
  assert.throws(() => artifactPath(out, "../escape.zip"), /escapes/);
  assert.throws(() => artifactPath(out, path.resolve("elsewhere.zip")), /escapes/);
  assert.throws(() => planArtifactPath(out, "nested/a.zip"), /plain filename/);
});

test("archive formats are inferred only from the two native suffixes", () => {
  assert.equal(formatFromFilename("source.zip"), "zip");
  assert.equal(formatFromFilename("source.tar.gz"), "tar.gz");
  assert.equal(formatFromFilename("source.tgz"), null);
  assert.equal(formatFromFilename("source.zip.txt"), null);
  assert.equal(formatLabel("zip"), "ZIP (.zip)");
  assert.throws(() => formatLabel("tgz"), /unsupported/);
});

test("refusal plans need the exact head and source identity", () => {
  const plan = parsePlan(refusalPlan());
  assert.equal(plan.phase, "refusal");
  assert.equal(plan.source.serverId, "srv-source-1");
  assert.throws(() => parsePlan(refusalPlan({ schema: "other" })), /schema/);
  assert.throws(() => parsePlan(refusalPlan({ head: "abc" })), /40-character/);
  assert.throws(() => parsePlan(refusalPlan({ phase: "boot" })), /want one of/);
  assert.throws(() => parsePlan(refusalPlan({ source: {} })), /serverId/);
  assert.throws(() => parsePlan(refusalPlan({ base: "http://10.0.0.5" })), /loopback/);
});

test("export plans must name exactly ZIP and tar.gz in order", () => {
  const plan = parsePlan(exportPlan());
  assert.deepEqual(plan.exports.map((entry) => entry.format), ["zip", "tar.gz"]);
  assert.throws(
    () =>
      parsePlan(
        exportPlan({ exports: [{ format: "zip", expectedLabel: "ZIP (.zip)" }] }),
      ),
    /ZIP and tar.gz/,
  );
  assert.throws(
    () =>
      parsePlan(
        exportPlan({
          exports: [
            { format: "zip", expectedLabel: "ZIP (.zip)" },
            { format: "zip", expectedLabel: "ZIP (.zip)" },
          ],
        }),
      ),
    /duplicated/,
  );
  assert.throws(
    () =>
      parsePlan(
        exportPlan({
          exports: [
            { format: "zip", expectedLabel: "TAR.GZ (.tar.gz)" },
            { format: "tar.gz", expectedLabel: "TAR.GZ (.tar.gz)" },
          ],
        }),
      ),
    /expectedLabel/,
  );
});

test("import plans bind both formats to a named remote node and distinct ports", () => {
  const plan = parsePlan(importPlan());
  assert.equal(plan.remoteNode.id, "node-remote");
  assert.deepEqual(plan.imports.map((entry) => entry.port), [25586, 25587]);
  assert.throws(() => parsePlan(importPlan({ remoteNode: {} })), /remoteNode/);
  assert.throws(
    () =>
      parsePlan(
        importPlan({
          imports: [
            { format: "zip", file: "../x.zip", expectedLabel: "ZIP (.zip)", name: "a", port: 1 },
            {
              format: "tar.gz",
              file: "x.tar.gz",
              expectedLabel: "TAR.GZ (.tar.gz)",
              name: "b",
              port: 2,
            },
          ],
        }),
      ),
    /plain evidence filename/,
  );
  assert.throws(
    () =>
      parsePlan(
        importPlan({
          imports: [
            { format: "zip", file: "a.zip", expectedLabel: "ZIP (.zip)", name: "a", port: 25586 },
            {
              format: "tar.gz",
              file: "b.tar.gz",
              expectedLabel: "TAR.GZ (.tar.gz)",
              name: "b",
              port: 25586,
            },
          ],
        }),
      ),
    /distinct/,
  );
});

test("preview facts require the rendered archive panel and a clean finding list", () => {
  const facts = previewFacts(
    "Archive preview ZIP (.zip) Schema 1 · exporter 2.0.1 · 9 files · 1.2 MB expanded No compatibility findings.",
  );
  assert.equal(facts.looksLikePreview, true);
  assert.equal(facts.saysNoFindings, true);
  assert.equal(facts.exporterMentioned, true);
  const blocked = previewFacts("Archive preview TAR.GZ (.tar.gz) This archive has blocking findings.");
  assert.equal(blocked.saysNoFindings, false);
});

test("the driver record schema is stable for the Python verifier", () => {
  assert.equal(RECORD_SCHEMA, "archive-ui-acceptance-run-v1");
  assert.equal(PLAN_SCHEMA, "archive-ui-acceptance-plan-v1");
});

// Regression: the copy audit rewrote the login placeholders, which used to be
// the driver's selectors, and the refusal phase timed out before signing in.
// Keep the driver on the stable attributes the login form actually renders.
test("login selectors are stable test ids the login page exposes", async () => {
  const loginPage = await fs.readFile(
    new URL("../web/carbon-panel/src/routes/login/+page.svelte", import.meta.url),
    "utf8",
  );
  const seen = [];
  for (const selector of [LOGIN_SELECTORS.username, LOGIN_SELECTORS.password]) {
    const match = /^input\[data-testid="([a-z0-9-]+)"\]$/.exec(selector);
    assert.ok(match, `login selector ${selector} must select an input by data-testid`);
    const testId = match[1];
    assert.match(
      loginPage,
      new RegExp(`data-testid="${testId}"`),
      `login page must expose ${testId}`,
    );
    seen.push(testId);
  }
  assert.deepEqual(seen, ["auth-login-username", "auth-login-password"]);
  assert.doesNotMatch(
    `${LOGIN_SELECTORS.username} ${LOGIN_SELECTORS.password}`,
    /placeholder/,
    "login selectors must not depend on placeholder copy",
  );
});

test("readHead reports the exact checkout", () => {
  const observed = readHead("0".repeat(40));
  assert.match(observed.observed, /^[0-9a-f]{40}$/);
  assert.equal(observed.match, false);
  assert.equal(typeof observed.worktreeClean, "boolean");
});

test("the created server id comes from the post-create /servers/<uuid> URL", () => {
  const id = "3f2a6c1e-9b4d-4e7a-8c5f-1d2e3a4b5c6d";
  assert.equal(serverIdFromUrl(`/servers/${id}`), id);
  assert.equal(serverIdFromUrl(`/servers/${id}/`), id);
  assert.equal(serverIdFromUrl(`http://127.0.0.1:18080/servers/${id}?tab=overview`), id);
  assert.equal(serverIdFromUrl(new URL(`http://127.0.0.1:18080/servers/${id}#files`)), id);
  // Canonical ids are lowercase even if the URL is not.
  assert.equal(serverIdFromUrl(`/servers/${id.toUpperCase()}`), id);
  for (const unrelated of [
    "",
    "/",
    "/servers",
    "/servers/new",
    "/servers/srv-source-1",
    `/servers/${id}/console`,
    `/server/${id}`,
    `/servers/${id}-extra`,
    "/servers/not-a-uuid",
    "/servers/%E0%A4%A",
  ]) {
    assert.equal(serverIdFromUrl(unrelated), null, `accepted ${unrelated}`);
  }
});

test("the created id is persisted before the screenshot and survives a capture failure", async () => {
  const outDir = await fs.mkdtemp(path.join(os.tmpdir(), "archive-ui-record-"));
  const id = "5b1c2d3e-4f60-4718-9a2b-3c4d5e6f7081";
  try {
    const record = {
      schema: RECORD_SCHEMA,
      phase: "import",
      imports: [],
      screenshots: [],
      error: null,
    };
    const entry = {
      format: "zip",
      name: "archive-ui-zip",
      file: "source.zip",
      expectedLabel: "ZIP (.zip)",
      port: 25586,
      createdServerId: id,
      createdRoute: `/servers/${id}`,
    };
    const events = [];
    let persistedAtCapture = null;
    await assert.rejects(
      recordCreatedImport({
        record,
        entry,
        persist: async () => {
          events.push("persist");
          await writeDriverRecord(record, outDir, "import");
        },
        capture: async () => {
          events.push("capture");
          persistedAtCapture = JSON.parse(
            await fs.readFile(path.join(outDir, "import-run.json"), "utf8"),
          );
          throw new Error("screenshot failed");
        },
      }),
      /screenshot failed/,
    );

    // The id reached disk before the screenshot started, and the in-memory
    // partial record still carries it for the driver's failure path.
    assert.deepEqual(events, ["persist", "capture"]);
    assert.deepEqual(
      persistedAtCapture.imports.map((saved) => saved.createdServerId),
      [id],
    );
    assert.equal(record.imports.length, 1);
    assert.equal(record.imports[0].createdServerId, id);
    assert.equal(record.imports[0].createdRoute, `/servers/${id}`);

    // Re-persisting the failed record (what main does on error) keeps the id.
    await writeDriverRecord(record, outDir, "import");
    const partial = JSON.parse(
      await fs.readFile(path.join(outDir, "import-run.json"), "utf8"),
    );
    assert.deepEqual(
      partial.imports.map((saved) => saved.createdServerId),
      [id],
    );

    // Updating the same format is idempotent: it must not add a duplicate.
    upsertImportEntry(record, { format: "zip", port: 25587 });
    assert.equal(record.imports.length, 1);
    assert.equal(record.imports[0].port, 25587);
    assert.equal(record.imports[0].createdServerId, id);
    await assert.rejects(
      recordCreatedImport({ record, entry: { format: "zip" } }),
      /persist callback/,
    );
  } finally {
    await fs.rm(outDir, { recursive: true, force: true });
  }
});
