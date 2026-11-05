#!/usr/bin/env python3
"""Collect the exact native archives tested for an annotated release tag."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess

from ci_native import ROOT, TARGETS, build_version, sha256


def check_tag(version, source):
    build_version(version)
    if version == "dev":
        raise ValueError("a release requires a vMAJOR.MINOR.PATCH tag")
    ref = "refs/tags/" + version
    def git(*args):
        return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()
    if git("cat-file", "-t", ref) != "tag":
        raise ValueError("release tags must be annotated with release notes")
    if git("rev-parse", ref + "^{commit}") != source:
        raise ValueError("release tag does not match the tested source commit")
    if not git("for-each-ref", "--format=%(contents:subject)", ref):
        raise ValueError("release tag has no release notes")


def collect(version, source, artifacts, output):
    reports = sorted(artifacts.glob("**/report.json"))
    if len(reports) != len(TARGETS):
        raise ValueError("one successful native report is required for every release target")
    targets, archives = set(), []
    for path in reports:
        report = json.loads(path.read_text(encoding="utf-8"))
        target = report.get("target")
        if target not in TARGETS or target in targets:
            raise ValueError("missing, duplicate or unexpected release target")
        if (report.get("status") != "passed" or report.get("source_commit") != source
                or report.get("source_modified") is not False or report.get("version") != version
                or not report.get("tests", {}).get("required_passed")):
            raise ValueError("archive was not successfully tested at this clean source and version")
        targets.add(target)
        system, arch = target.split("/")
        name = f"vista_{version}_{system}_{arch}" + (".zip" if system == "windows" else ".tar.gz")
        if report.get("archive") != name:
            raise ValueError("archive name does not match its version and target")
        archive = path.parent / "dist" / name
        checksum = sha256(archive)
        if checksum != report.get("archive_sha256") or archive.stat().st_size != report.get("archive_size"):
            raise ValueError("archive differs from the bytes tested on its native runner")
        archives.append((archive, checksum))
    # Validate every report before producing any public release assets.
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        raise ValueError("release output directory must be empty")
    for archive, _ in archives:
        shutil.copyfile(archive, output / archive.name)
    checksums = "".join(f"{checksum}  {archive.name}\n" for archive, checksum in sorted(archives))
    (output / "SHA256SUMS").write_text(checksums, encoding="utf-8")
    return checksums


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", type=build_version, required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--check-tag", action="store_true")
    parser.add_argument("--artifacts", type=Path, default=ROOT / ".vista/ci/artifacts")
    parser.add_argument("--output", type=Path, default=ROOT / ".vista/ci/release")
    args = parser.parse_args()
    check_tag(args.version, args.source)
    if not args.check_tag:
        print(collect(args.version, args.source, args.artifacts, args.output), end="")


if __name__ == "__main__":
    main()
