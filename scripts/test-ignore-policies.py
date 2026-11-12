#!/usr/bin/env python3
"""Check Git exclusions and, with --docker, real clean/cluttered build contexts."""

import argparse
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent

EXCLUDED = (
    "data/world/level.dat", "nested/Data/world.dat", "backups/server.zip", "Backups/server.zip",
    "worlds/world/level.dat", "Worlds/world/level.dat", "servers/server/level.dat",
    ".env", ".env.local", "private.env", "secrets/token.key", "secrets/tls.pem",
    "secrets/client.p12", "secrets/client.pfx", "nested/.mcp.json",
    ".mcp.local.json", ".cline/mcp.json", "nested/.cline/mcp.json", ".cursor/mcp.json", "nested/.cursor/mcp.json",
    ".zed/mcp.json", ".zed/settings.local.json", ".claude/config.json", "nested/.claude/config.json",
    ".vs/settings", "nested/.idea/workspace.xml", "developer.suo", "config.yaml", "panel.db",
    "panel.db-wal", "nested/cache.sqlite3", "nested/cache.sqlite3-wal", ".worktrees/other/source.go",
    "worktrees/other/source.go", "nested/.worktrees/other/source.go", "tmp/compiler.exe", "nested/node_modules/lib.js",
    "nested/.terraform/provider", "nested/.cache/local", "artifacts/build-output.bin", "local-worker.exe",
    "web/carbon-panel/.svelte-kit/output.js", ".website-build/index.html",
)
INCLUDED = (
    ".envrc", ".mcp.json", ".env.example", "config.example.yaml",
    "internal/backupcrypt/encrypt.go", "cmd/carbon-panel/main.go",
    "cmd/carbon-storage/main.go", "proto/carbonpanel/v1/backup.proto",
    "pkg/proto/carbonpanel/v1/backup.pb.go", "web/carbon-panel/package.json",
    "web/carbon-panel/build/.gitkeep", "web/carbon-panel/src/routes/servers/+page.svelte",
)


def run(*args, **kwargs):
    subprocess.run(args, check=True, **kwargs)


def check_git_policy(directory):
    run("git", "init", "--quiet", str(directory))
    shutil.copy(ROOT / ".gitignore", directory / ".gitignore")
    frontend = directory / "web/carbon-panel"
    frontend.mkdir(parents=True)
    shutil.copy(ROOT / "web/carbon-panel/.gitignore", frontend / ".gitignore")
    for expected, paths in ((True, EXCLUDED), (False, INCLUDED)):
        for path in paths:
            result = subprocess.run(
                ["git", "-c", "core.excludesFile=", "check-ignore", "-q", "--no-index", path],
                cwd=directory, check=False,
            )
            if result.returncode not in (0, 1) or (result.returncode == 0) != expected:
                raise AssertionError(f"Git policy: {path} expected ignored={expected}; exit={result.returncode}")
    print(f"PASS: Git ignore matrix ({len(EXCLUDED)} excluded, {len(INCLUDED)} included)")


def file_hashes(directory):
    return {str(path.relative_to(directory)).replace(os.sep, "/"): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in directory.rglob("*") if path.is_file()}


def check_docker_context(directory):
    clean, clutter = directory / "clean", directory / "clutter"
    clean.mkdir()
    archive = directory / "source.tar"
    # git.exe is useful when running Linux Docker against a Windows Git worktree.
    git = os.environ.get("GIT_ARCHIVE_EXECUTABLE", "git")
    with archive.open("wb") as output:
        run(git, "archive", "--format=tar", "HEAD", cwd=ROOT, stdout=output)
    with tarfile.open(archive) as source:
        source.extractall(clean, filter="data")
    shutil.copy(ROOT / ".dockerignore", clean / ".dockerignore")
    (clean / ".env.example").write_text("PUBLIC_EXAMPLE=true\n", encoding="utf-8")
    shutil.copytree(clean, clutter)
    for path in EXCLUDED:
        marker = clutter / path
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text("synthetic private canary", encoding="utf-8")
    for path in (".git/config", "nested/.git/config", ".mcp.json", ".envrc"):
        marker = clutter / path
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text("synthetic checkout metadata", encoding="utf-8")
    dockerfile = directory / "Dockerfile"
    dockerfile.write_text("FROM scratch\nCOPY . /\n", encoding="utf-8")
    outputs = []
    for context in (clean, clutter):
        target = directory / (context.name + "-out")
        run("docker", "buildx", "build", "--progress=plain", "-f", str(dockerfile),
            "--output", f"type=local,dest={target}", str(context))
        outputs.append(file_hashes(target))
    if outputs[0] != outputs[1]:
        differences = sorted(set(outputs[0].items()) ^ set(outputs[1].items()))
        raise AssertionError(f"Clean and cluttered Docker contexts differ: {differences[:20]}")
    for required in ("internal/backupcrypt/encrypt.go", "cmd/carbon-storage/main.go",
                     "cmd/carbon-panel/main.go", "proto/carbonpanel/v1/backup.proto",
                     "pkg/proto/carbonpanel/v1/backup.pb.go", "config.example.yaml",
                     "web/carbon-panel/package.json", "scripts/web-build.sh", ".env.example"):
        if required not in outputs[0]:
            raise AssertionError(f"Required source missing from Docker context: {required}")
    print(f"PASS: clean and cluttered Docker contexts match ({len(outputs[0])} files)")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--docker", action="store_true", help="also build and compare isolated Docker contexts")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="carbon-ignore-policy-") as temp:
        base = Path(temp)
        check_git_policy(base / "git")
        if args.docker:
            check_docker_context(base)


if __name__ == "__main__":
    main()
