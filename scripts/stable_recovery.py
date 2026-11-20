# SPDX-License-Identifier: MPL-2.0
"""Recover the prior Stable channel from one authenticated publication run."""
import argparse
import hashlib
import json
import os
from pathlib import Path

from scripts import docs_publication_gate as pages
from scripts import release


def index_identity(site):
    data = (Path(site) / "version-index.json").read_bytes()
    if (Path(site) / "docs/version-index.json").read_bytes() != data:
        raise ValueError("The two index routes disagree")
    stable = json.loads(data)["channels"]["stable"]
    return {"tag": stable["tag"], "commit": stable["commit"],
            "digest": stable["image"]["digest"], "index": hashlib.sha256(data).hexdigest()}


def capture(args):
    prior, target = index_identity(args.prior), index_identity(args.candidate)
    receipt = json.loads(os.environ["PRIOR"])
    candidate = json.loads(os.environ["CANDIDATE"])
    if (receipt.get("verified") is not True or receipt.get("channel") != "stable"
            or (receipt["revision"], receipt["digest"]) != (prior["commit"], prior["digest"])
            or (candidate["release_tag"], candidate["commit"], candidate["digest"]) !=
               (target["tag"], target["commit"], target["digest"])):
        raise ValueError("Rendered indexes disagree with the qualified transaction")
    record_id = int(os.environ["RELEASE_ID"])
    if record_id <= 0 or not all(os.environ[key].isdecimal() for key in ("PRIOR_DEPLOYMENT", "PRIOR_ARTIFACT")):
        raise ValueError("Prior deployment, artifact or prepared release identity is unknown")
    journal = {"schema": 1, "run": os.environ["GITHUB_RUN_ID"],
               "workflow_sha": os.environ["GITHUB_SHA"], "repo": os.environ["GITHUB_REPOSITORY"],
               "prior_deployment": os.environ["PRIOR_DEPLOYMENT"],
               "prior_artifact": os.environ["PRIOR_ARTIFACT"], "record": record_id,
               "prior": prior, "target": target, "prior_receipt": receipt}
    Path(args.journal).write_text(json.dumps(journal, indent=2) + "\n", encoding="utf-8")


def authorize(journal, run, repository, run_id, record, latest, prior, target):
    """Only the original interrupted main dispatch may restore its recorded bytes."""
    if (journal.get("schema") != 1 or journal["repo"] != repository or journal["run"] != run_id
            or str(run["id"]) != run_id or run["repository"]["full_name"] != repository
            or run["head_repository"]["full_name"] != repository
            or run["head_sha"] != journal["workflow_sha"] or run["head_branch"] != "main"
            or run["path"] != ".github/workflows/release.yml" or run["event"] != "workflow_dispatch"
            or run["status"] != "completed" or run["conclusion"] not in ("failure", "cancelled", "timed_out")):
        raise ValueError("Recovery requires the exact interrupted trusted release dispatch")
    if (record.get("draft") is not True or record.get("id") != journal["record"]
            or record.get("tag_name") != target["tag"] or latest.get("tag_name") != prior["tag"]
            or latest.get("draft") is not False or latest.get("prerelease") is not False):
        raise ValueError("The published release state has changed or the prepared draft is unavailable")
    if journal["prior"] != prior or journal["target"] != target or prior == target:
        raise ValueError("Retained Pages artifacts do not identify this transition")


def classify_aliases(prior, target, observe):
    missing = []
    advanced = []
    for alias in ("latest", "stable"):
        observations = release._coerce_observation_map(observe(alias))
        for registry in release.CHANNEL_REGISTRIES:
            state, _ = observations.get(registry, ("unknown", {}))
            if state == "absent" and alias == "stable":
                missing.append(registry)
                continue
            for item in (prior, target):
                ok, _ = release.channel_oci_parity(observations, item["digest"], item["commit"], (registry,))
                if ok:
                    if item == target:
                        advanced.append((registry, alias))
                    break
            else:
                raise ValueError(f"Unrecognized live {registry} {alias} identity")
    if missing and advanced:
        raise ValueError("Missing Stable alias after candidate promotion began")
    return bool(missing), bool(advanced)


def recover(args):
    env = os.environ
    journal = json.loads(Path(args.journal).read_text(encoding="utf-8"))
    prior, target = index_identity(args.prior), index_identity(args.candidate)
    repo, run_id = env["GITHUB_REPOSITORY"], env["ORIGINAL_RUN"]
    if not run_id.isdecimal() or env.get("GITHUB_REF") != "refs/heads/main":
        raise ValueError("Recovery requires a numeric source run and trusted main")
    api, root = pages.github_api(repo)
    def read(suffix):
        return json.loads(api(root + suffix))

    authorize(journal, read("/actions/runs/" + run_id), repo, run_id,
              release.get_release(target["tag"]), read("/releases/latest"), prior, target)
    verified, reason = release.exact_main_ci_verified(target["commit"])
    if not verified:
        raise ValueError("Candidate is missing exact main CI: " + reason)
    for item in (prior, target):
        version = release.resolve_panel_version(item["tag"])
        good, reason, _ = release.release_provenance(version, item["commit"], item["digest"],
            git_tag=item["tag"], allow_prepared_draft=item == target)
        if not good:
            raise ValueError("Release provenance refused: " + reason)
        observed = release._coerce_observation_map(release.channel_observe(version[1:]))
        ok, reason = release.channel_oci_parity(observed, item["digest"], item["commit"])
        if not ok:
            raise ValueError("Immutable registry identity refused: " + reason)
    baseline, reason = pages.pages_baseline(repo)
    if not baseline:
        raise ValueError("Active Pages deployment is unknown: " + reason)
    live_hash = hashlib.sha256(pages._http_get("https://carbonpanel.dev")("/version-index.json")).hexdigest()
    if live_hash not in (prior["index"], target["index"]):
        raise ValueError("Live Pages bytes belong to another transaction")
    live_site = args.prior if live_hash == prior["index"] else args.candidate
    ok, problems = pages.verify_prior_bytes(live_site, "https://carbonpanel.dev")
    if not ok:
        raise ValueError("Live Pages artifacts changed: " + ", ".join(problems))
    owned = baseline["run_id"] == run_id
    missing, advanced = classify_aliases(prior, target, release.channel_observe)
    if not owned and baseline["deployment_id"] != journal["prior_deployment"]:
        if live_site == args.prior and not advanced:
            release.write_output({"restore_pages": "false"})
            return
        raise ValueError("Active Pages deployment is owned by another publisher")
    if missing:
        release.establish_stable_baseline_cli(argparse.Namespace(receipt=json.dumps(journal["prior_receipt"])))
    elif advanced:
        release.channel_rollback(journal["prior_receipt"], apply=True, candidate_digest=target["digest"])
    if any(classify_aliases(prior, target, release.channel_observe)):
        raise ValueError("Prior Stable/latest registry identities did not converge")
    release.write_output({"restore_pages": str(owned).lower()})


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("journal", "recover"))
    parser.add_argument("--journal", required=True)
    parser.add_argument("--prior", required=True)
    parser.add_argument("--candidate", required=True)
    args = parser.parse_args()
    (capture if args.command == "journal" else recover)(args)
