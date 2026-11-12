#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Build, inspect and publish versioned Carbon Panel release bundles."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
from datetime import datetime, timezone
import zipfile

ROOT = Path(__file__).resolve().parent.parent
IMAGE = "atharvnaphade/carbon-panel"
TARGETS = (("linux", "amd64"), ("linux", "arm64"), ("darwin", "amd64"),
           ("darwin", "arm64"), ("windows", "amd64"))
SEMVER = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?")


def version(value):
    match = SEMVER.fullmatch(value)
    if not match or any(part.isdigit() and len(part) > 1 and part[0] == "0"
                        for part in (match[4] or "").split(".")):
        raise ValueError("Use vMAJOR.MINOR.PATCH or a SemVer prerelease, such as v3.0.0-beta.5")
    return value


def run(*args, **kwargs):
    kwargs.setdefault("cwd", ROOT)
    return subprocess.check_output(args, text=True, **kwargs).strip()


def archive_name(tag, goos, goarch):
    return f"carbon-panel_{version(tag)}_{goos}_{goarch}" + (".zip" if goos == "windows" else ".tar.gz")


def github(*args):
    return run("gh", *args)


def guard_publishing(tag, commit):
    subprocess.run(["git", "merge-base", "--is-ancestor", commit, "origin/main"], check=True)
    runs = json.loads(github("run", "list", "--workflow", "ci.yml", "--event", "push",
                              "--branch", "main", "--commit", commit,
                              "--json", "status,conclusion", "--limit", "1"))
    if not runs or runs[0] != {"conclusion": "success", "status": "completed"}:
        raise ValueError("Publishing requires successful main CI for this exact commit")
    tags = run("git", "tag", "--list", tag)
    if tags and run("git", "rev-parse", f"{tag}^{{commit}}") != commit:
        raise ValueError("The version tag already points at a different commit")
    releases = json.loads(github("api", "--paginate", "--slurp", "/repos/{owner}/{repo}/releases?per_page=100"))
    if any(release["tag_name"] == tag and not release["draft"]
           for page in releases for release in page):
        raise ValueError("A published release is immutable; choose a new version")


def plan(args):
    tag = version(args.version)
    commit = run("git", "rev-parse", "HEAD")
    epoch = int(run("git", "show", "-s", "--format=%ct", "HEAD"))
    if args.publish:
        guard_publishing(tag, commit)
    values = {"version": tag, "commit": commit, "epoch": str(epoch),
              "date": datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
              "prerelease": str("-" in tag).lower(), "publish": str(args.publish).lower()}
    print(json.dumps(values, indent=2))
    if os.environ.get("GITHUB_OUTPUT"):
        with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
            output.writelines(f"{key}={value}\n" for key, value in values.items())


def write_archive(destination, members, epoch):
    """Stable order, timestamps, ownership and permissions; no workstation paths."""
    if destination.suffix == ".zip":
        date = datetime.fromtimestamp(max(epoch, 315532800), timezone.utc).timetuple()[:6]
        with zipfile.ZipFile(destination, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
            for name, data, mode in sorted(members):
                entry = zipfile.ZipInfo(name, date)
                entry.create_system = 3
                entry.external_attr = (0o100000 | mode) << 16
                archive.writestr(entry, data, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    else:
        with destination.open("wb") as output, gzip.GzipFile(filename="", mode="wb", fileobj=output, mtime=epoch) as compressed:
            with tarfile.open(fileobj=compressed, mode="w") as archive:
                for name, data, mode in sorted(members):
                    entry = tarfile.TarInfo(name)
                    entry.size, entry.mode, entry.mtime = len(data), mode, epoch
                    archive.addfile(entry, io.BytesIO(data))


def read_archive(path):
    if path.suffix == ".zip":
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            if len(set(names)) != len(names):
                raise ValueError("Duplicate archive member")
            members = {name: archive.read(name) for name in names}
    else:
        with tarfile.open(path) as archive:
            entries = archive.getmembers()
            if any(not item.isfile() for item in entries) or len({item.name for item in entries}) != len(entries):
                raise ValueError("Only unique regular files are allowed in release bundles")
            members = {item.name: archive.extractfile(item).read() for item in entries}
    if any("/" in name or "\\" in name or name in ("", ".", "..") for name in members):
        raise ValueError("Release bundles must contain flat, named files")
    return members


def package(args):
    tag = version(args.version)
    target = (args.goos, args.goarch)
    if target not in TARGETS:
        raise ValueError(f"Unsupported platform: {target}")
    commit = run("git", "rev-parse", "HEAD")
    if subprocess.run(["git", "diff", "--quiet", "HEAD", "--"], cwd=ROOT).returncode:
        raise ValueError("Release builds require a clean tracked tree")
    frontend = ROOT / "web/carbon-panel/build"
    if not (frontend / "index.html").is_file() or not (frontend / "_app").is_dir():
        raise ValueError("Build the complete, versioned frontend before packaging")
    if not any(tag.encode() in asset.read_bytes() for asset in (frontend / "_app").rglob("*.js")):
        raise ValueError("The embedded web UI does not contain the release version")
    epoch = int(args.epoch)
    date = datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    info = {"version": tag, "commit": commit, "built": date,
            "os": args.goos, "arch": args.goarch, "go": run("go", "version")}
    environment = {**os.environ, "CGO_ENABLED": "0", "GOOS": args.goos, "GOARCH": args.goarch}
    flags = f"-s -w -X github.com/athNdev/carbon-panel/internal/buildinfo.Version={tag} -X github.com/athNdev/carbon-panel/internal/buildinfo.Commit={commit} -X github.com/athNdev/carbon-panel/internal/buildinfo.Date={date}"
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    members = []
    with tempfile.TemporaryDirectory(prefix="carbon-release-") as temporary:
        for program in ("carbon-panel", "carbon-storage"):
            name = program + (".exe" if args.goos == "windows" else "")
            binary = Path(temporary) / name
            subprocess.run(["go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags,
                            "-o", str(binary), f"./cmd/{program}"], cwd=ROOT, env=environment, check=True)
            # Each release target runs on a matching native runner. This checks
            # the actual shipped CLI without configuration, Docker or data I/O.
            reported = run(str(binary), "--version", cwd=temporary)
            if not reported.startswith(f"{program} {tag} ") or commit not in reported:
                raise ValueError(f"Incorrect embedded version: {reported}")
            print(reported, flush=True)
            members.append((name, binary.read_bytes(), 0o755))
    for name in ("config.example.yaml", "LICENSE", "LICENSE.MPL-2.0"):
        members.append((name, (ROOT / name).read_bytes(), 0o644))
    members.append(("README.md", (ROOT / "docs/release-bundle.md").read_bytes(), 0o644))
    members.append(("BUILD-INFO.json", (json.dumps(info, indent=2) + "\n").encode(), 0o644))
    destination = output / archive_name(tag, *target)
    write_archive(destination, members, epoch)
    # Verify that packaging preserved both executables and all metadata.
    if read_archive(destination) != {name: data for name, data, _ in members}:
        raise ValueError("Archive did not preserve bundle contents")
    print(destination)


def sha256(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def collect(args):
    tag = version(args.version)
    source, output = Path(args.source), Path(args.output)
    output.mkdir(parents=True, exist_ok=True)
    image = output.parent / "image"
    image.mkdir(parents=True, exist_ok=True)
    manifest = {"version": tag, "commit": args.commit, "image": IMAGE + ":" + tag[1:], "artifacts": []}
    for goos, goarch in TARGETS:
        name = archive_name(tag, goos, goarch)
        path = source / name
        members = read_archive(path)
        info = json.loads(members["BUILD-INFO.json"])
        if (info["version"], info["commit"], info["os"], info["arch"]) != (tag, args.commit, goos, goarch):
            raise ValueError(f"Mismatched release input: {name}")
        suffix = ".exe" if goos == "windows" else ""
        required = {"carbon-panel" + suffix, "carbon-storage" + suffix, "BUILD-INFO.json",
                    "config.example.yaml", "LICENSE", "LICENSE.MPL-2.0", "README.md"}
        if set(members) != required:
            raise ValueError(f"Incomplete or unexpected bundle: {name}")
        shutil.copyfile(path, output / name)
        manifest["artifacts"].append({"name": name, "sha256": sha256(path), "os": goos, "arch": goarch})
        if goos == "linux":
            platform = image / f"linux_{goarch}"
            platform.mkdir(parents=True, exist_ok=True)
            for program in ("carbon-panel", "carbon-storage"):
                (platform / program).write_bytes(members[program])
                (platform / program).chmod(0o755)
    shutil.copyfile(ROOT / "config.example.yaml", image / "config.example.yaml")
    shutil.copyfile(ROOT / "web/carbon-panel/static/schemav1.yaml", output / "openapi.yaml")
    (output / "release-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    finalize(argparse.Namespace(output=str(output), digest=None))


def finalize(args):
    output = Path(args.output)
    path = output / "release-manifest.json"
    manifest = json.loads(path.read_text(encoding="utf-8"))
    if args.digest:
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", args.digest):
            raise ValueError("Missing or invalid image digest")
        manifest["image_digest"] = args.digest
        path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    checksums = "".join(f"{sha256(file)}  {file.name}\n" for file in sorted(output.iterdir())
                        if file.is_file() and file.name != "checksums.txt")
    (output / "checksums.txt").write_text(checksums, encoding="utf-8")


def verify_image(args):
    manifest = json.loads(Path(args.manifest).read_text(encoding="utf-8"))
    platforms = {(item.get("platform", {}).get("os"), item.get("platform", {}).get("architecture"))
                 for item in manifest.get("manifests", [])}
    platforms.discard(("unknown", "unknown"))  # BuildKit provenance descriptors
    if platforms != {("linux", "amd64"), ("linux", "arm64")}:
        raise ValueError(f"Published image must contain both Linux architectures: {platforms}")


def publish(args):
    tag = version(args.version)
    guard_publishing(tag, args.commit)
    output = Path(args.output)
    manifest = json.loads((output / "release-manifest.json").read_text(encoding="utf-8"))
    if manifest["version"] != tag or manifest["commit"] != args.commit or not manifest.get("image_digest"):
        raise ValueError("Release artifacts and published image must match the release commit")
    expected = {archive_name(tag, *target) for target in TARGETS} | {"openapi.yaml", "release-manifest.json"}
    lines = (output / "checksums.txt").read_text(encoding="utf-8").splitlines()
    recorded = dict((line.split("  ", 1)[1], line.split("  ", 1)[0]) for line in lines)
    if set(recorded) != expected or any(sha256(output / name) != digest for name, digest in recorded.items()):
        raise ValueError("Release files do not match their checksums")
    if not run("git", "tag", "--list", tag):
        github("api", "repos/{owner}/{repo}/git/refs", "-f", f"ref=refs/tags/{tag}", "-f", f"sha={args.commit}")
    generated = json.loads(github("api", "repos/{owner}/{repo}/releases/generate-notes", "-f", f"tag_name={tag}", "-f", f"target_commitish={args.commit}"))
    channel = "This is a beta/prerelease build.\n\n" if "-" in tag else ""
    body = (f"{channel}## Downloads\n\nChoose the archive for your operating system and CPU. Each includes the panel with its web UI, the storage helper, configuration example and licenses.\n\n"
            f"Verify downloads against **checksums.txt**. **release-manifest.json** records the exact source commit and Docker image digest. Back up your database and configuration before upgrading; preserve server worlds and existing data.\n\n"
            f"Docker: `{manifest['image']}` (Linux amd64 and arm64).\n\n"
            f"[Installation and verification](https://github.com/{os.environ['GITHUB_REPOSITORY']}/blob/{tag}/docs/releases.md)\n\n" + generated["body"])
    with tempfile.TemporaryDirectory(prefix="carbon-release-notes-") as temporary:
        notes = Path(temporary) / "notes.md"
        notes.write_text(body, encoding="utf-8")
        # Only drafts are reusable; the guard above refuses published versions.
        exists = subprocess.run(["gh", "release", "view", tag], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
        if not exists:
            github("release", "create", tag, "--verify-tag", "--draft", "--title", f"Carbon Panel {tag}", "--notes-file", str(notes))
        github("release", "edit", tag, "--notes-file", str(notes))
        github("release", "upload", tag, *(str(output / name) for name in sorted(expected | {"checksums.txt"})), "--clobber")
        uploaded = json.loads(github("api", f"repos/{{owner}}/{{repo}}/releases/tags/{tag}"))
        remote = {asset["name"]: asset.get("digest") for asset in uploaded["assets"]}
        complete = expected | {"checksums.txt"}
        if not uploaded["draft"] or set(remote) != complete or any(
                remote[name] != "sha256:" + sha256(output / name) for name in complete):
            raise ValueError("Uploaded draft assets do not match the verified local release")
    github("release", "edit", tag, "--draft=false", f"--prerelease={str('-' in tag).lower()}", f"--latest={str('-' not in tag).lower()}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    planner = commands.add_parser("plan")
    planner.add_argument("--version", required=True)
    planner.add_argument("--publish", action="store_true")
    builder = commands.add_parser("package")
    for name in ("version", "goos", "goarch", "epoch", "output"):
        builder.add_argument("--" + name, required=True)
    collector = commands.add_parser("collect")
    for name in ("version", "commit", "source", "output"):
        collector.add_argument("--" + name, required=True)
    finisher = commands.add_parser("finalize")
    finisher.add_argument("--output", required=True)
    finisher.add_argument("--digest")
    image_checker = commands.add_parser("verify-image")
    image_checker.add_argument("--manifest", required=True)
    publisher = commands.add_parser("publish")
    for name in ("version", "commit", "output"):
        publisher.add_argument("--" + name, required=True)
    args = parser.parse_args()
    globals()[args.command.replace("-", "_")](args)


if __name__ == "__main__":
    main()
