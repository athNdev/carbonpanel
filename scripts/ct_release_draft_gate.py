#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Guarded checks for the controller-ct artifact-only draft release.

GitHub's REST GET /releases/tags/<tag> does not return draft releases. The
release-id API URL is read from the authenticated `gh release view` JSON, which
uses GraphQL and does see drafts. Never promote a release unless the immutable
view is still a draft for the exact tag and exact target commit, and the assets
attached to that release-id URL have the exact names and GitHub digests.
"""
import argparse
import json
import re
import sys

ASSET_NAMES = (
    "carbonpanel-controller-ct.tar.zst",
    "carbonpanel-controller-ct.tar.zst.sha256",
    "package-receipt.json",
)


class GateError(RuntimeError):
    pass


def draft_api_url(view, version, sha):
    if not isinstance(view, dict):
        raise GateError("release view is not a JSON object")
    if view.get("isDraft") is not True:
        raise GateError("release is not a draft; refusing to publish")
    if view.get("tagName") != version:
        raise GateError("release tag does not match the dispatch version")
    if view.get("targetCommitish") != sha:
        raise GateError("release target commit does not match github.sha")
    api = view.get("apiUrl") or ""
    if re.fullmatch(r"https://api\.github\.com/repos/[^/]+/[^/]+/releases/\d+", api) is None:
        raise GateError("release apiUrl is not an immutable release-id API URL")
    return api


def verify_assets(assets_text, archive_sha, checksum_sha, receipt_sha):
    expected = {
        ASSET_NAMES[0]: "sha256:" + archive_sha,
        ASSET_NAMES[1]: "sha256:" + checksum_sha,
        ASSET_NAMES[2]: "sha256:" + receipt_sha,
    }
    got = {}
    for line in assets_text.splitlines():
        line = line.strip()
        if not line:
            continue
        name, _, digest = line.partition(" ")
        if name in got:
            raise GateError("duplicate release asset: " + name)
        got[name] = digest
    if set(got) != set(expected):
        raise GateError("release asset name set mismatch")
    for name, digest in expected.items():
        if got[name] != digest:
            raise GateError("release asset digest mismatch: " + name)
    return dict(sorted(expected.items()))


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="command", required=True)
    d = sub.add_parser("draft-url", help="validate the draft view and print its release-id API URL")
    d.add_argument("--view", required=True, help="JSON from gh release view --json ...")
    d.add_argument("--version", required=True)
    d.add_argument("--sha", required=True)
    a = sub.add_parser("verify-assets", help="verify exact release asset names and digests")
    a.add_argument("--assets", required=True, help="newline-separated '<name> <digest>' from gh api")
    a.add_argument("--archive-sha", required=True)
    a.add_argument("--checksum-sha", required=True)
    a.add_argument("--receipt-sha", required=True)
    args = p.parse_args(argv)
    try:
        if args.command == "draft-url":
            print(draft_api_url(json.loads(args.view), args.version, args.sha))
        else:
            print(json.dumps(verify_assets(args.assets, args.archive_sha,
                                           args.checksum_sha, args.receipt_sha),
                             sort_keys=True))
    except (GateError, ValueError, TypeError) as exc:
        print("error: " + str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
