# SPDX-License-Identifier: MPL-2.0
"""Bounded Nightly recovery from independent Hub, GHCR and Pages baselines."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from scripts import docs_publication_gate as pages, openapi_version, release

HUB = "atharvnaphade/carbonpanel"
GHCR = "ghcr.io/athndev/carbonpanel"
REPOSITORY = "athNdev/carbonpanel"
SITE = "https://carbonpanel.dev"
GUIDE = "getting-started/proxmox/"
PLATFORMS = {"amd64", "arm64"}
PLAN = "nightly-recovery-plan"
# State tuples are (Hub, GHCR, Pages). Each request permits one mutation.
TRANSITIONS = {
    "stage": (("prior", "absent", "prior"), ("prior", "absent", "stage"), "stage"),
    "hub": (("prior", "absent", "stage"), ("candidate", "absent", "stage"), "hub"),
    "docs": (("candidate", "absent", "stage"), ("candidate", "absent", "final"), "final"),
    "commit": (("candidate", "absent", "final"), ("candidate", "candidate", "final"), "ghcr"),
    "restore-hub": (("candidate", "absent", "stage"), ("prior", "absent", "stage"), "hub-prior"),
    "restore-docs": (("prior", "absent", "stage"), ("prior", "absent", "prior"), "prior"),
}
# Rollback before the GHCR commit point is valid with either staged or final docs.
FINAL_ROLLBACK = {
    "restore-hub": (("candidate", "absent", "final"), ("prior", "absent", "final"), "hub-prior"),
    "restore-docs": (("prior", "absent", "final"), ("prior", "absent", "prior"), "prior"),
}
MIXED_ROLLBACK = {
    "restore-hub": (("candidate", "absent", "mixed"), ("prior", "absent", "mixed"), "hub-prior"),
    "restore-docs": (("prior", "absent", "mixed"), ("prior", "absent", "prior"), "prior"),
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def run(*command):
    return subprocess.check_output(command, text=True, timeout=180)


def api(path):
    return json.loads(run("gh", "api", f"repos/{REPOSITORY}/{path}"))


def validate_revision(commit):
    require(re.fullmatch(r"[0-9a-f]{40}", commit), "A full source commit is required")


def ancestor(older, newer):
    validate_revision(older)
    validate_revision(newer)
    require(subprocess.run(["git", "merge-base", "--is-ancestor", older, newer],
                           check=False).returncode == 0,
            f"{newer} does not descend from {older}")


def verify_ci(commit):
    validate_revision(commit)
    verified, reason = release.exact_main_ci_verified(commit)
    require(verified, reason)
    runs = api(f"actions/workflows/ci.yml/runs?branch=main&event=push&head_sha={commit}&per_page=1")
    latest = runs["workflow_runs"][0]
    require((latest["head_sha"], latest["head_branch"], latest["event"],
             latest["status"], latest["conclusion"]) ==
            (commit, "main", "push", "completed", "success"), "Exact main CI changed")
    return latest["id"]


def require_pages_settled(run_id):
    """Stop if a previous deployment can still update the public site."""
    current_run = f"/actions/runs/{run_id}/"
    for deployment in api("deployments?environment=github-pages&per_page=10"):
        statuses = api(f"deployments/{deployment['id']}/statuses?per_page=1")
        require(statuses, f"Pages deployment {deployment['id']} has no status")
        latest = statuses[0]
        target = (latest.get("target_url") or "").rstrip("/") + "/"
        if current_run in target:
            continue
        require(latest["state"] in ("success", "failure", "error", "inactive"),
                f"Pages deployment {deployment['id']} is still unsettled")
        if latest["state"] in ("failure", "error"):
            match = re.search(r"/actions/runs/(\d+)/", target)
            require(match, "A failed environment deployment has no resolvable run")
            require_failed_pages_run_settled(match[1])
        if latest["state"] == "success":
            break


def require_failed_pages_run_settled(run_id):
    # The environment job can fail after the provider accepted a deployment.
    # Reconcile the native action and, if needed, the real Pages status first.
    jobs = api(f"actions/runs/{run_id}/jobs?filter=latest&per_page=100")["jobs"]
    steps = [step for job in jobs for step in job.get("steps", [])
             if step["name"] == "Deploy the selected Pages artifact"]
    if len(steps) == 1 and steps[0]["conclusion"] in ("success", "skipped"):
        return
    deployment_id = pages_provider_id(run_id)
    provider_status = api(f"pages/deployments/{deployment_id}")["status"]
    require(provider_status in ("succeed", "deployment_failed", "deployment_perms_error",
                                "deployment_content_failed", "deployment_cancelled"),
            f"Pages deployment {deployment_id} has unconfirmed status {provider_status}")


def pages_provider_id(run_id):
    logs = run("gh", "run", "view", run_id, "--repo", REPOSITORY, "--log")
    identifiers = set(re.findall(r"Created deployment for [0-9a-f]{40}, ID: ([0-9a-zA-Z_-]+)", logs))
    require(len(identifiers) == 1,
            f"Pages outcome for run {run_id} is unknown; resolve the provider deployment before writing")
    return identifiers.pop()


def fetch(url, headers=None, *, exact_route=False):
    headers = {"User-Agent": "carbonpanel-nightly-recovery", **(headers or {})}
    request = urllib.request.Request(url, headers=headers)
    if "Authorization" in headers:
        request.remove_header("Authorization")
        request.add_unredirected_header("Authorization", headers["Authorization"])
    with urllib.request.urlopen(request, timeout=30) as response:
        if exact_route:
            require(urllib.parse.urlsplit(response.geturl())[:3] == urllib.parse.urlsplit(url)[:3],
                    f"Unexpected redirect for {url.split('?')[0]}")
        return response.read()


class Registry:
    """Read the same manifest graph that unauthenticated public clients see."""

    def __init__(self, host, repository, token_url):
        self.root = f"https://{host}/v2/{repository}"
        token = json.loads(fetch(token_url))["token"]
        self.headers = {
            "Authorization": "Bearer " + token,
            "Accept": ("application/vnd.oci.image.index.v1+json, "
                       "application/vnd.oci.image.manifest.v1+json, "
                       "application/vnd.docker.distribution.manifest.list.v2+json, "
                       "application/vnd.docker.distribution.manifest.v2+json"),
        }

    def document(self, kind, reference, allow_missing=False):
        try:
            data = fetch(f"{self.root}/{kind}/{reference}", self.headers)
        except urllib.error.HTTPError as error:
            if (allow_missing and error.code == 404
                    and [item["code"] for item in json.load(error)["errors"]]
                    == ["MANIFEST_UNKNOWN"]):
                return None, None
            raise
        actual = digest(data)
        if release.valid_digest(reference):
            require(actual == reference, f"{kind} content differs from digest {reference}")
        return actual, json.loads(data)

    def tag(self, reference):
        return self.document("manifests", reference, allow_missing=True)[0]

    def image(self, index_digest, commit, *, with_provenance=True):
        validate_revision(commit)
        _, index = self.document("manifests", index_digest)
        children = {}
        attestations = {}
        for descriptor in index["manifests"]:
            platform = descriptor["platform"]
            annotations = descriptor.get("annotations", {})
            if platform["os"] == "linux" and platform["architecture"] in PLATFORMS:
                arch = platform["architecture"]
                require(arch not in children, f"Duplicate Linux {arch} manifest")
                children[arch] = descriptor["digest"]
            else:
                require((platform["os"], platform["architecture"]) == ("unknown", "unknown")
                        and annotations.get("vnd.docker.reference.type") == "attestation-manifest",
                        "Unexpected descriptor in the image index")
                child = annotations["vnd.docker.reference.digest"]
                require(child not in attestations, "Duplicate attached provenance manifest")
                attestations[child] = descriptor["digest"]
        require(set(children) == PLATFORMS, "Required Linux platform images are missing")
        if with_provenance:
            require(set(children.values()) == set(attestations), "Image provenance manifests are missing")
        evidence = {}
        for arch, child in sorted(children.items()):
            _, manifest = self.document("manifests", child)
            _, config = self.document("blobs", manifest["config"]["digest"])
            labels = config["config"]["Labels"]
            require((config["os"], config["architecture"]) == ("linux", arch)
                    and labels["org.opencontainers.image.revision"] == commit
                    and labels["org.opencontainers.image.source"].lower()
                    == f"https://github.com/{REPOSITORY.lower()}",
                    f"Linux {arch} image config does not bind the expected source")
            evidence[arch] = {"manifest": child}
            if not with_provenance:
                continue
            attestation = attestations[child]
            _, attested_manifest = self.document("manifests", attestation)
            layers = [
                layer["digest"] for layer in attested_manifest["layers"]
                if layer.get("annotations", {}).get("in-toto.io/predicate-type")
                in ("https://slsa.dev/provenance/v1", "https://slsa.dev/provenance/v0.2")
            ]
            require(len(layers) == 1, f"Linux {arch} has no unique BuildKit provenance")
            _, statement = self.document("blobs", layers[0])
            verify_provenance(statement, child, commit)
            evidence[arch]["provenance"] = layers[0]
        return evidence


def verify_provenance(statement, child, commit):
    require(any(subject.get("digest", {}).get("sha256") == child.removeprefix("sha256:")
                for subject in statement["subject"]), "Provenance subject mismatch")
    predicate = statement["predicate"]
    kind = statement["predicateType"]
    if kind == "https://slsa.dev/provenance/v1":
        definition = predicate["buildDefinition"]
        request = definition["externalParameters"]["request"]
        source = request["root"]["request"]["args"]
        build_type = definition["buildType"]
    elif kind == "https://slsa.dev/provenance/v0.2":
        build_type = predicate["buildType"]
        source = predicate["invocation"]["parameters"]["args"]
    else:
        raise ValueError("Unexpected provenance statement type")
    require(build_type.startswith("https://github.com/moby/buildkit/")
            and source.get("vcs:revision") == commit
            and source.get("vcs:source", "").lower().removesuffix(".git")
            == f"https://github.com/{REPOSITORY.lower()}",
            "BuildKit provenance does not bind the exact repository revision")


def registries():
    return (
        Registry("registry-1.docker.io", HUB,
                 f"https://auth.docker.io/token?service=registry.docker.io&scope=repository:{HUB}:pull"),
        Registry("ghcr.io", GHCR.removeprefix("ghcr.io/"),
                 "https://ghcr.io/token?service=ghcr.io&scope=repository:athndev/carbonpanel:pull"),
    )


def verify_build_run(run_id, commit, image_digest):
    """Corroborate BuildKit metadata with the trusted CD run's exact push log."""
    require(re.fullmatch(r"[1-9][0-9]*", str(run_id)), "A CD build run ID is required")
    record = api(f"actions/runs/{run_id}")
    require((record["path"], record["head_branch"], record["head_sha"], record["conclusion"])
            == (".github/workflows/cd.yml", "main", commit, "success")
            and record["event"] in ("schedule", "workflow_dispatch", "workflow_run")
            and record["head_repository"]["full_name"].lower() == REPOSITORY.lower(),
            "The image build receipt is not a successful trusted-main CD run")
    logs = run("gh", "run", "view", str(run_id), "--repo", REPOSITORY, "--log")
    # BuildKit prints this only for a pushed manifest; a workflow input or an
    # alias log containing the expected digest cannot qualify a build.
    pattern = (rf"pushing manifest for \S+:sha-{commit}@{re.escape(image_digest)}"
               r" [0-9.]+s done(?:\r?$)")
    require(re.search(pattern, logs, re.MULTILINE), "CD logs lack the exact immutable push")


def qualify_images(config, hub, ghcr):
    prior, candidate = config["hub"], config["candidate"]
    for item in (prior, candidate):
        validate_revision(item["commit"])
        require(release.valid_digest(item["digest"]), "An exact image digest is required")
    require(prior["digest"] != candidate["digest"], "Recovery requires a distinct candidate")
    prior_ref, candidate_ref = (f"sha-{item['commit']}" for item in (prior, candidate))
    require(hub.tag(prior_ref) == prior["digest"], "Prior Hub immutable reference changed")
    require(hub.tag(candidate_ref) == ghcr.tag(candidate_ref) == candidate["digest"],
            "Candidate immutable references lack exact digest parity")
    return {
        "hub_prior": hub.image(prior["digest"], prior["commit"]),
        "hub_candidate": hub.image(candidate["digest"], candidate["commit"]),
        "ghcr_candidate": ghcr.image(candidate["digest"], candidate["commit"]),
    }


def route_files(prefix):
    return {
        prefix + "": prefix + "index.html",
        prefix + GUIDE: prefix + GUIDE + "index.html",
        prefix + "api/": prefix + "api/index.html",
        prefix + "schemav1.yaml": prefix + "schemav1.yaml",
    }


def prefix_for(commit):
    return f"/docs/candidate/{commit}/"


def get_live(path):
    # Unique URL on each probe prevents a warm CDN entry from qualifying a write.
    url = SITE + path + "?nightly_recovery=" + uuid.uuid4().hex
    try:
        return fetch(url, {"Cache-Control": "no-cache"}, exact_route=True)
    except urllib.error.HTTPError as error:
        if (error.code == 404 and
                urllib.parse.urlsplit(error.geturl())[:3] == urllib.parse.urlsplit(url)[:3]):
            return None
        raise


def read(root, relative):
    return (root / relative.lstrip("/")).read_bytes()


def marker(index, name):
    entry = index["channels"][name]
    require(entry["image"]["revision"] == entry["commit"], f"{name} marker revision drifted")
    return {"commit": entry["commit"], "digest": entry["image"]["digest"]}


def stable_identity(index):
    entry = index["channels"]["stable"]
    return {
        **marker(index, "stable"),
        "tag": entry["tag"],
        "version": entry["version"],
    }


def verify_stable(index, hub, ghcr):
    stable = stable_identity(index)
    expected = stable["digest"]
    product = release.product_release_or_none(stable["tag"])
    legacy = (stable["tag"] == release.LEGACY_STABLE_TAG
              and stable["version"] == release.LEGACY_STABLE_TAG.removeprefix("v"))
    require(release.valid_digest(expected)
            and (legacy or (product and not product["prerelease"] and product["semver"] == stable["version"])),
            "Stable docs do not carry a qualified version and digest")
    aliases = {tag: {"docker_hub": hub.tag(tag), "ghcr": ghcr.tag(tag)}
               for tag in ("latest", "stable")}
    parity = {"docker_hub": expected, "ghcr": expected}
    require(aliases["latest"] == parity and
            (aliases["stable"] == parity or (legacy and all(value is None
                                                            for value in aliases["stable"].values()))),
            "Public Stable/latest aliases disagree with the Stable docs")
    return {**stable, "aliases": aliases,
            "platforms": {
                "docker_hub": hub.image(expected, stable["commit"], with_provenance=False),
                "ghcr": ghcr.image(expected, stable["commit"], with_provenance=False),
            }}


def check_routes(site, prefix, schema, *, guide_source=False):
    for route, filename in route_files(prefix).items():
        data = read(site, filename)
        if filename.endswith(".yaml"):
            openapi_version.verify_text(data.decode("utf-8"), schema)
        else:
            require(b"<html" in data.lower(), f"Expected rendered HTML at {route}")
    if guide_source:
        require(b"raw.githubusercontent.com/athNdev/carbonpanel/main/scripts/proxmox/install-carbon-panel.sh"
                in read(site, prefix + GUIDE + "index.html"), "Nightly Proxmox guide lacks install source")


def site_probes(site, commit):
    files = {"/version-index.json": "/version-index.json",
             "/docs/version-index.json": "/docs/version-index.json",
             "/channel-marker.json": "/channel-marker.json",
             **route_files("/docs/"), **route_files("/docs/nightly/")}
    candidate = prefix_for(commit)
    files.update({candidate + "marker.json": candidate + "marker.json", **route_files(candidate)})
    probes = {}
    for url, file in files.items():
        path = site / file.lstrip("/")
        if url.startswith(candidate) and not path.is_file():
            probes[url] = None
        else:
            probes[url] = digest(path.read_bytes())
    return probes


def observe_pages(plan, get=get_live):
    paths = plan["probes"]["prior"]
    observed = {}
    for path in paths:
        data = get(path)
        observed[path] = None if data is None else digest(data)
    matches = [name for name, expected in plan["probes"].items() if expected == observed]
    if len(matches) == 1:
        return matches[0], observed
    require(all(any(expected[path] == value for expected in plan["probes"].values())
                for path, value in observed.items()), "Public Pages returned bytes outside the plan")
    return "mixed", observed


def observe(plan, hub, ghcr, get=get_live):
    prior = plan["config"]["hub"]["digest"]
    target = plan["config"]["candidate"]["digest"]
    aliases = (hub.tag("nightly"), ghcr.tag("nightly"))
    mapping = ({prior: "prior", target: "candidate"}, {None: "absent", target: "candidate"})
    require(all(alias in states for alias, states in zip(aliases, mapping)),
            "Nightly alias is outside the plan; investigate without changing it")
    docs, probes = observe_pages(plan, get)
    state = (mapping[0][aliases[0]], mapping[1][aliases[1]], docs)
    return state, dict(zip(("docker_hub", "ghcr"), aliases)), probes


def decide(phase, state):
    if phase == "inspect":
        return None, state
    if phase == "restore-hub" and state == ("prior", "absent", "prior"):
        return None, state
    options = [TRANSITIONS[phase]]
    for alternative in (FINAL_ROLLBACK, MIXED_ROLLBACK):
        if phase in alternative:
            options.append(alternative[phase])
    # Any verified post-state means the requested write already landed.
    for _, after, _ in options:
        if state == after:
            return None, after
    for before, after, action in options:
        if state == before:
            return action, after
    raise ValueError(f"{phase} cannot run from {state}; no action is authorized")


def tree_digest(site):
    files = []
    for path in sorted(site.rglob("*")):
        require(not path.is_symlink(), "Pages artifact contains a symlink")
        relative = path.relative_to(site)
        require(not {".git", ".github"}.intersection(relative.parts),
                "Pages uploader excludes a path from this artifact")
        if path.is_file():
            files.append((relative.as_posix(), digest(path.read_bytes())))
    return digest(json.dumps(files).encode())


def build_site(args, candidate, state, stable):
    env = {**os.environ, "CARBONPANEL_STABLE_TAG": stable["tag"],
           "CARBONPANEL_STABLE_REVISION": stable["commit"], "CARBONPANEL_STABLE_DIGEST": stable["digest"]}
    if stable["tag"] == release.LEGACY_STABLE_TAG:
        # The renderer's overrides accept canonical product tags only. The
        # verified legacy release can use the checked-in source if it is exact.
        tracked = stable_identity(json.loads(Path("docs/website-source.json").read_bytes()))
        require(tracked == {key: stable[key] for key in tracked},
                "Tracked legacy Stable source differs from the verified public release")
        for key in ("CARBONPANEL_STABLE_TAG", "CARBONPANEL_STABLE_REVISION", "CARBONPANEL_STABLE_DIGEST"):
            env.pop(key)
    receipt = f"nightly-recovery-{args.run}"
    subprocess.run(["python3", "scripts/build_website.py",
                    "--nightly-revision", candidate["commit"], "--nightly-digest", candidate["digest"],
                    "--promotion-state", state, "--promotion-receipt", receipt],
                   check=True, env=env)
    subprocess.run(["python3", "scripts/check_website.py",
                    "--expect-nightly-revision", candidate["commit"],
                    "--expect-nightly-digest", candidate["digest"],
                    "--expect-promotion-state", state, "--expect-promotion-receipt", receipt],
                   check=True, env=env)
    return Path(".website-build/site")


def verify_final_index(index, stable, candidate):
    # build_site validates the rendered channel contract against both exact
    # pinned identities. Recheck the assembled final artifact before binding.
    actual_stable = stable_identity(index)
    require(actual_stable == {key: stable[key] for key in actual_stable},
            "Final artifact changes the publicly verified Stable identity")
    require(marker(index, "nightly") == {key: candidate[key] for key in ("commit", "digest")}
            and index["promotion"]["state"] == "final", "Final marker does not bind the candidate")


def preserve_prior(root):
    baseline, reason = pages.pages_baseline(REPOSITORY)
    require(baseline and baseline["artifact_verified"], reason)
    return preserve_pages(root, baseline)


def preserve_pages(root, baseline):
    reader, endpoint = pages.github_api(REPOSITORY)
    run_id = baseline["run_id"]
    available = json.loads(reader(f"{endpoint}/actions/runs/{run_id}/artifacts?per_page=100"))
    selected = [item for item in available["artifacts"]
                if str(item["id"]) in baseline["artifact_candidates"]]
    result = pages.fetch_prior_pages(
        root, selected, lambda item: reader(f"{endpoint}/actions/artifacts/{item['id']}/zip"),
        get_live("/version-index.json"), run_id=run_id)
    return {**result, "deployment_id": baseline["deployment_id"], "deployment_sha": baseline["sha"]}


def verify_final_deployment(plan):
    # An idempotent retry may finish the environment job without itself
    # deploying. Identify the native Pages write, even if its runner failed
    # after the provider accepted it.
    for deployment in api("deployments?environment=github-pages&per_page=10"):
        statuses = api(f"deployments/{deployment['id']}/statuses?per_page=1")
        if not statuses:
            continue
        match = re.search(r"/actions/runs/(\d+)(?:/|$)", statuses[0].get("target_url") or "")
        if not match:
            continue
        run_id = match[1]
        jobs = api(f"actions/runs/{run_id}/jobs?filter=latest&per_page=100")["jobs"]
        if not any(job["name"] == "Recover split Nightly (docs)" and
                   any(step["name"] == "Deploy the selected Pages artifact" and
                       step["conclusion"] not in (None, "skipped") for step in job.get("steps", []))
                   for job in jobs):
            continue
        provider_id = pages_provider_id(run_id)
        require(api(f"pages/deployments/{provider_id}")["status"] == "succeed",
                "The observed final Pages deployment has not succeeded at the provider")
        listing = api(f"actions/runs/{run_id}/artifacts?per_page=100")
        artifacts = [str(item["id"]) for item in listing["artifacts"]
                     if item["name"] == "github-pages" and not item["expired"]]
        require(len(artifacts) == 1, "Final Pages deployment artifact is not uniquely available")
        baseline = {"deployment_id": str(deployment["id"]), "run_id": run_id,
                    "artifact_candidates": artifacts, "sha": deployment["sha"]}
        with tempfile.TemporaryDirectory(prefix="nightly-final-pages-") as temporary:
            site = Path(temporary) / "site"
            evidence = preserve_pages(site, baseline)
            require(tree_digest(site) == plan["trees"]["final"],
                    "Successful Pages deployment artifact differs from the reviewed final tree")
        return {**evidence, "provider_deployment_id": provider_id}
    raise ValueError("No native Pages write was observed for the reviewed final site")


def prepare(args, hub, ghcr):
    config = json.loads(args.config)
    for name in ("hub", "candidate", "docs"):
        validate_revision(config[name]["commit"])
        require(release.valid_digest(config[name]["digest"]), f"{name} needs an exact digest")
    prior, candidate = config["hub"], config["candidate"]
    require(os.environ["GITHUB_SHA"] == run("git", "rev-parse", "HEAD").strip()
            == api("commits/main")["sha"] == candidate["commit"],
            "Prepare checkout, current main HEAD and candidate must agree")
    ancestor(prior["commit"], candidate["commit"])
    ancestor(config["docs"]["commit"], candidate["commit"])
    require(hub.tag("nightly") == prior["digest"] and ghcr.tag("nightly") is None,
            "Expected split Hub/GHCR baseline changed")
    ci = {"hub": verify_ci(prior["commit"]), "candidate": verify_ci(candidate["commit"])}
    for item in (prior, candidate):
        verify_build_run(item["build_run"], item["commit"], item["digest"])
    images = qualify_images(config, hub, ghcr)
    root = args.directory
    sites = {name: root / name for name in ("prior", "stage", "final")}
    require_pages_settled(args.run)
    baseline = preserve_prior(sites["prior"])
    prior_index = json.loads(read(sites["prior"], "/version-index.json"))
    require(marker(prior_index, "nightly") == config["docs"], "Pages baseline changed")
    stable = verify_stable(prior_index, hub, ghcr)
    candidate_path = prefix_for(candidate["commit"]).strip("/")
    require(not (sites["prior"] / candidate_path).exists(), "Candidate route already exists")
    schema = f"0.0.0-nightly.{candidate['commit'][:12]}"
    stable_schema = stable["version"]
    for channel, label in (("docs", stable_schema),
                           ("docs/nightly", f"0.0.0-nightly.{config['docs']['commit'][:12]}")):
        check_routes(sites["prior"], f"/{channel}/", label)
    offpath = build_site(args, candidate, "candidate", stable) / candidate_path
    shutil.copytree(sites["prior"], sites["stage"])
    shutil.copytree(offpath, sites["stage"] / candidate_path)
    candidate_marker = {"channel": "nightly", "candidate_commit": candidate["commit"],
                        "candidate_digest": candidate["digest"], "canonical": False}
    (sites["stage"] / candidate_path / "marker.json").write_text(
        json.dumps(candidate_marker) + "\n", encoding="utf-8")
    check_routes(sites["stage"], prefix_for(candidate["commit"]), schema, guide_source=True)
    shutil.copytree(build_site(args, candidate, "final", stable), sites["final"])
    shutil.copytree(sites["stage"] / candidate_path, sites["final"] / candidate_path)
    verify_final_index(json.loads(read(sites["final"], "/version-index.json")), stable, candidate)
    check_routes(sites["final"], "/docs/", stable_schema)
    check_routes(sites["final"], "/docs/nightly/", schema, guide_source=True)
    probes = {name: site_probes(site, candidate["commit"]) for name, site in sites.items()}
    require(all(value == probes["stage"][route] for route, value in probes["prior"].items()
                if not route.startswith(prefix_for(candidate["commit"]))),
            "Staging changed canonical bytes")
    plan = {"version": 1, "run": args.run, "config": config, "ci": ci, "images": images, "stable": stable,
            "pages_baseline": baseline, "probes": probes,
            "trees": {name: tree_digest(site) for name, site in sites.items()}}
    state, aliases, _ = observe(plan, hub, ghcr)
    require(state == ("prior", "absent", "prior"),
            "Live surfaces changed during preparation")
    plan["prior_aliases"] = aliases
    plan_bytes = (json.dumps(plan, sort_keys=True, indent=2) + "\n").encode()
    (root / "plan.json").write_bytes(plan_bytes)
    print(f"Prepared {PLAN}; run {args.run}, plan digest {digest(plan_bytes)}")


def load_plan(args):
    run_id = args.plan_run
    require(re.fullmatch(r"[1-9][0-9]*", run_id) and release.valid_digest(args.plan_digest),
            "The exact plan run and digest are required")
    origin = api(f"actions/runs/{run_id}")
    require((origin["path"], origin["head_branch"], origin["event"], origin["conclusion"])
            == (".github/workflows/cd.yml", "main", "workflow_dispatch", "success"),
            "Plan did not come from a successful main CD dispatch")
    if not args.directory.exists():
        run("gh", "run", "download", run_id, "--repo", REPOSITORY,
            "--name", PLAN, "--dir", str(args.directory))
    data = (args.directory / "plan.json").read_bytes()
    require(digest(data) == args.plan_digest, "Plan digest differs from reviewed plan")
    plan = json.loads(data)
    require(plan["version"] == 1 and str(plan["run"]) == run_id, "Plan identity mismatch")
    for name in ("prior", "stage", "final"):
        require(tree_digest(args.directory / name) == plan["trees"][name], f"{name} site bytes changed")
    return plan


def phase_check(args, plan, hub, ghcr):
    require(qualify_images(plan["config"], hub, ghcr) == plan["images"],
            "Digest-addressed image evidence changed")
    state, _, _ = observe(plan, hub, ghcr)
    action, expected = decide(args.phase, state)
    check_stable(args, plan, hub, ghcr)
    if action and args.phase in ("stage", "hub", "docs", "commit"):
        candidate = plan["config"]["candidate"]["commit"]
        require(api("commits/main")["sha"] == candidate, "Candidate is no longer main HEAD")
        verify_ci(candidate)
    if action:
        require_pages_settled(args.run)
        if action == "ghcr":
            verify_final_deployment(plan)
        refreshed, _, _ = observe(plan, hub, ghcr)
        require(refreshed in (state, expected), "Nightly surfaces changed before the write")
        state = refreshed
        action, expected = decide(args.phase, state)
    return state, action, expected


def check_stable(args, plan, hub, ghcr):
    baseline = json.loads(read(args.directory / "prior", "/version-index.json"))
    require(verify_stable(baseline, hub, ghcr) == plan["stable"],
            "Public Stable state changed since the reviewed plan")


def write_output(key, value):
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write(f"{key}={value}\n")


def verify_after(args, plan, hub, ghcr):
    expected = tuple(args.expected.split(","))
    require(len(expected) == 3, "Missing planned post-write state")
    last = None
    for attempt in range(8):
        try:
            observed, aliases, probes = observe(plan, hub, ghcr)
            require(observed == expected, f"Observed {observed}; expected {expected}")
            break
        except (ValueError, OSError) as error:
            last = error
            if attempt == 7:
                raise ValueError("Mutation not yet confirmed. Inspect using the same plan.") from last
            time.sleep(10)
    evidence = qualify_images(plan["config"], hub, ghcr)
    require(evidence == plan["images"], "Immutable image evidence changed")
    check_stable(args, plan, hub, ghcr)
    final = observed == ("candidate", "candidate", "final")
    deployment = None
    if final:
        deployment = verify_final_deployment(plan)
    receipt = {"plan": args.plan_digest, "run": args.run, "phase": args.phase,
               "state": dict(zip(("docker_hub", "ghcr", "pages"), observed)),
               "observed_at": datetime.now(timezone.utc).isoformat(),
               "prior_aliases": plan["prior_aliases"], "observed_aliases": aliases,
               "config": plan["config"], "images": evidence, "ci": plan["ci"], "stable": plan["stable"],
               "pages_baseline": plan["pages_baseline"], "probes": probes,
               "pages_final": deployment,
               "verified_final": final}
    Path("nightly-recovery-receipt.json").write_text(json.dumps(receipt, indent=2) + "\n",
                                                    encoding="utf-8")
    print(json.dumps(receipt))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "decide", "alias", "verify"))
    parser.add_argument("--phase", choices=(*TRANSITIONS, "inspect"))
    parser.add_argument("--config", default="")
    parser.add_argument("--plan-run", default="")
    parser.add_argument("--plan-digest", default="")
    parser.add_argument("--directory", type=Path, default=Path(".nightly-recovery"))
    parser.add_argument("--expected", default="")
    args = parser.parse_args()
    args.run = os.environ["GITHUB_RUN_ID"]
    require(os.environ.get("GITHUB_REPOSITORY", "").lower() == REPOSITORY.lower()
            and os.environ.get("GITHUB_REF") == "refs/heads/main"
            and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch",
            "Nightly recovery requires a trusted-main dispatch")
    hub, ghcr = registries()
    if args.operation == "prepare":
        prepare(args, hub, ghcr)
        return
    plan = load_plan(args)
    if args.operation == "verify":
        verify_after(args, plan, hub, ghcr)
        return
    _, action, expected = phase_check(args, plan, hub, ghcr)
    if args.operation == "decide":
        write_output("action", action or "")
        write_output("expected", ",".join(expected))
        write_output("pages_tree", expected[2])
        return
    if action is None:
        return
    repository, source = {
        "hub": (HUB, "candidate"), "hub-prior": (HUB, "hub"), "ghcr": (GHCR, "candidate"),
    }[action]
    target = plan["config"][source]["digest"]
    subprocess.run(["docker", "buildx", "imagetools", "create",
                    "--tag", f"{repository}:nightly", f"{repository}@{target}"], check=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        raise SystemExit(f"Nightly recovery refused or unconfirmed: {error}") from error
