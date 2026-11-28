#!/usr/bin/env python3
"""Verify a GitHub release candidate and collect its exact native archives."""

import argparse
from datetime import datetime
import json
from pathlib import Path
import re
import shutil
import subprocess
from urllib.parse import urlencode

from ci_native import ROOT, TARGETS, build_version, sha256
from package_release import archive_name


CANDIDATE_STEPS = {
    "Go and frontend checks": {
        "Check release safeguards", "Check Go, race safety, frontend types and units",
        "Check bundled license notices", "Check generated frontend files",
    },
    **{f"Native {target}": {
        "Build, unpack and run the Windows desktop application" if target.startswith("windows/")
        else "Build, unpack and run the native application",
    } for target in TARGETS},
    **{f"Linux browser {project}": {"Run viewport browser checks"}
       for project in ("desktop", "mobile-portrait", "mobile-landscape")},
    "Verify candidate archives": {"Verify all tested native archives"},
}


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def require_release_version(version):
    if build_version(version) == "dev":
        raise ValueError("a release requires a vMAJOR.MINOR.PATCH version")


def check_tag(version, source):
    require_release_version(version)
    ref = "refs/tags/" + version
    if git("cat-file", "-t", ref) != "tag":
        raise ValueError("release tags must be annotated with release notes")
    if git("rev-parse", ref + "^{commit}") != source:
        raise ValueError("release tag does not match the tested source commit")
    if not git("for-each-ref", "--format=%(contents:subject)", ref):
        raise ValueError("release tag has no release notes")


def github_entries(endpoint, field, **query):
    parameters = urlencode({**query, "per_page": 100})
    pages = json.loads(subprocess.check_output([
        "gh", "api", "--paginate", "--slurp", f"{endpoint}?{parameters}",
    ], text=True))
    entries = [entry for page in pages for entry in page[field]]
    if not pages or pages[0]["total_count"] > len(entries):
        raise ValueError("GitHub returned an incomplete candidate history")
    return entries


def clean_candidate_jobs(jobs):
    # GitHub includes the tag-only publication job even on a candidate dispatch.
    publication_name = "Publish tested archives"
    publication = [job for job in jobs if job["name"] == publication_name]
    if len(publication) > 1 or any(
        job["status"] != "completed" or job["conclusion"] != "skipped"
        or any(step["conclusion"] != "skipped" for step in job["steps"])
        for job in publication
    ):
        return False
    jobs = [job for job in jobs if job["name"] != publication_name]
    if len(jobs) != len(CANDIDATE_STEPS) or {job["name"] for job in jobs} != CANDIDATE_STEPS.keys():
        return False
    return all(job["status"] == "completed" and job["conclusion"] == "success"
               and CANDIDATE_STEPS[job["name"]] <= {step["name"] for step in job["steps"]
                                                    if step["conclusion"] == "success"}
               for job in jobs)


def find_candidate(repo, version, source, tagged_at=None):
    require_release_version(version)
    if not re.fullmatch(r"[\w.-]+/[\w.-]+", repo, re.ASCII) or not re.fullmatch(r"[0-9a-f]{40}", source):
        raise ValueError("candidate lookup requires OWNER/REPO and a full commit SHA")
    actions = f"repos/{repo}/actions"
    runs = github_entries(f"{actions}/workflows/validate.yml/runs", "workflow_runs",
                          event="workflow_dispatch", head_sha=source)
    eligible = []
    for run in runs:
        title = re.fullmatch(r"Release candidate (v[0-9]+\.[0-9]+\.[0-9]+)", run["display_title"])
        if (run["event"] != "workflow_dispatch" or run["head_sha"] != source
                or (run.get("head_repository") or {}).get("full_name", "").lower() != repo.lower()
                or not title):
            continue
        try:
            build_version(title[1])
        except argparse.ArgumentTypeError:
            continue  # A mistyped version is not a test of this source.
        matching_version = title[1] == version
        run_id, attempts = run["id"], run["run_attempt"]
        if run["status"] != "completed":
            if matching_version:
                raise ValueError(f"candidate run {run_id} is not complete")
            continue
        # Do not erase this source's test failures by retrying or renaming the version.
        for attempt in range(1, attempts + 1):
            jobs = github_entries(f"{actions}/runs/{run_id}/attempts/{attempt}/jobs", "jobs")
            for job in jobs:
                checked = CANDIDATE_STEPS.get(job["name"], ())
                for step in job["steps"]:
                    if step["name"] in checked and (step["conclusion"] in {"failure", "timed_out"}
                            or step["conclusion"] == "cancelled" and job["conclusion"] == "timed_out"):
                        raise ValueError(f"candidate run {run_id}, attempt {attempt}: {job['name']} / {step['name']} failed; fix and validate a new source commit")
            if matching_version and attempts == 1 and run["conclusion"] == "success" and clean_candidate_jobs(jobs):
                completed_at = datetime.fromisoformat(run["updated_at"]).timestamp()
                if tagged_at is None or completed_at <= tagged_at:
                    eligible.append(run_id)
    native_names = sorted(f"native-{target.replace('/', '-')}" for target in TARGETS)
    for run_id in sorted(eligible, reverse=True):
        artifacts = github_entries(f"{actions}/runs/{run_id}/artifacts", "artifacts")
        if sorted(item["name"] for item in artifacts if not item["expired"] and item["name"].startswith("native-")) == native_names:
            return run_id
    raise ValueError(f"no complete first-attempt candidate for {version} at {source} with retained native archives; run the full candidate check before signing the tag")


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
        native_desktop_test = {"windows": "TestNativeWindowsTray", "linux": "TestNativeLinuxTray",
                               "darwin": "TestNativeDarwinDesktop"}[system]
        if (not re.fullmatch(r"[0-9a-f]{64}", report.get("binary_sha256", ""))
                or report.get("cgo_enabled") is not False
                or "vista/desktop/" + native_desktop_test not in report["tests"]["required_passed"]):
            raise ValueError("release requires one tested non-CGO executable with native desktop coverage")
        if not {"vista/internal/launch/TestNativeApplication", "vista/internal/launch/TestNativeResetPort"} <= set(report["tests"]["required_passed"]):
            raise ValueError("release requires the tested service, Workers and restart paths")
        if system == "darwin" and report.get("distribution_verified") is not True:
            raise ValueError("macOS distribution requires verified signing and notarization")
        name = archive_name(version, system, arch)
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
    checksums = "".join(f"{checksum}  {archive.name}\n" for archive, checksum in sorted(archives, key=lambda item: item[0].name))
    (output / "SHA256SUMS").write_text(checksums, encoding="utf-8")
    return checksums


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", type=build_version, required=True)
    parser.add_argument("--source", required=True)
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument("--candidate", action="store_true", help="Verify candidate archives without a tag")
    modes.add_argument("--find-candidate", metavar="OWNER/REPO", help="Print the promotable GitHub run ID")
    parser.add_argument("--check-tag", action="store_true", help="Check only the tag, or require the candidate to predate it")
    parser.add_argument("--artifacts", type=Path, default=ROOT / ".vista/ci/artifacts")
    parser.add_argument("--output", type=Path, default=ROOT / ".vista/ci/release")
    args = parser.parse_args()
    if args.candidate and args.check_tag:
        parser.error("a candidate runs without a release tag")
    if args.check_tag or not (args.candidate or args.find_candidate):
        check_tag(args.version, args.source)
    if args.find_candidate:
        tagged_at = int(git("for-each-ref", "--format=%(taggerdate:unix)", f"refs/tags/{args.version}")) if args.check_tag else None
        print(find_candidate(args.find_candidate, args.version, args.source, tagged_at))
    elif not args.check_tag:
        require_release_version(args.version)
        print(collect(args.version, args.source, args.artifacts, args.output), end="")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error)) from None
