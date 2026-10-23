"""Run the pinned Harbor sample, optionally interleaving Vista binaries."""

import argparse
from datetime import datetime, timezone
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import sys

from benchmark_profile import digest, selected_profile
from summarize import summarize_run


HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
STATE = REPO / ".vista/benchmarks/harbor"
MANIFEST = json.loads((HERE / "tasks.json").read_text())
HARBOR_VERSION = "0.24.0"


def check_dataset(directory):
    revision = subprocess.check_output(["git", "-C", str(directory), "rev-parse", "HEAD"], text=True).strip()
    if revision != MANIFEST["commit"]:
        raise ValueError("Dataset revision differs from tasks.json")
    for name, expected in MANIFEST["files"].items():
        if digest(directory / "sample" / name) != expected:
            raise ValueError("Dataset file changed: " + name)
    return revision


def binaries(specs):
    result = {}
    for spec in specs:
        label, separator, name = spec.partition("=")
        if not separator or not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]*", label) or label in result:
            raise ValueError("Use a unique label=path for each --binary")
        path = Path(name).resolve()
        with path.open("rb") as stream:
            if stream.read(4) != b"\x7fELF":
                raise ValueError("Harbor task containers require a compatible Linux ELF binary")
        result[label] = {"path": str(path), "sha256": digest(path)}
    return result


def schedule(labels, tasks, rounds):
    entries = []
    for round_index in range(rounds):
        for task_index, task in enumerate(tasks):
            shift = (round_index + task_index) % len(labels)
            for label in labels[shift:] + labels[:shift]:
                entries.append({"round": round_index + 1, "task": task, "label": label,
                                "job": f"r{round_index + 1}-{task}-{label}"})
    return entries


def check_images(tasks):
    images = {}
    for task in tasks:
        tag = "ghcr.io/laude-institute/terminal-bench/" + task + ":2.0"
        expected = MANIFEST["runtime_images"][tag]
        result = subprocess.run(["docker", "image", "inspect", tag], capture_output=True, text=True)
        if result.returncode:
            raise ValueError(f"Missing benchmark image: docker pull {tag}")
        image = json.loads(result.stdout)[0]
        if expected not in (image.get("RepoDigests") or []):
            raise ValueError("Benchmark image differs from tasks.json: " + tag)
        images[tag] = {"id": image["Id"], "repo_digests": image["RepoDigests"]}
    return images


def command(entry, args, candidates, output):
    binary = candidates[entry["label"]]
    result = [sys.executable, "-m", "harbor.cli.main", "run", "-p", str(args.dataset / "sample" / entry["task"]),
              "-a", "vista_agent:VistaAgent", "-m", args.model,
              "--ak", "binary_path=" + binary["path"], "--ak", "profile_path=" + str(args.profile),
              "-n", "1", "-k", "1", "-r", "0", "-o", str(output / "jobs"), "--job-name", entry["job"]]
    if args.model_key:
        result += ["--ak", "model_key=" + args.model_key]
    if args.reasoning_effort is not None:
        result += ["--ak", "reasoning_effort=" + args.reasoning_effort]
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", action="append", required=True, metavar="LABEL=PATH")
    parser.add_argument("--profile", type=Path, required=True, help="Isolated source profile; only the selected model and credentials are copied")
    parser.add_argument("--model", required=True)
    parser.add_argument("--model-key")
    parser.add_argument("--reasoning-effort", help="Omit to use Vista's configured default")
    parser.add_argument("--dataset", type=Path, default=STATE / "dataset")
    parser.add_argument("--task", action="append", choices=MANIFEST["tasks"])
    parser.add_argument("--rounds", type=int, default=1)
    parser.add_argument("--output", type=Path, default=STATE / "runs" / datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ"))
    parser.add_argument("--dry-run", action="store_true", help="Validate inputs and print the schedule without starting Harbor or calling models")
    args = parser.parse_args()
    if args.rounds < 1:
        parser.error("--rounds must be positive")
    args.profile, args.dataset, args.output = args.profile.resolve(), args.dataset.resolve(), args.output.resolve()
    try:
        if args.output.exists():
            raise ValueError("Output directory already exists; choose a new run directory")
        if not args.output.is_relative_to(REPO / ".vista"):
            raise ValueError("Store benchmark outputs under .vista/")
        candidates = binaries(args.binary)
        revision = check_dataset(args.dataset)
        selected, credentials = selected_profile(args.profile, args.model, args.model_key)
        profile_files = [args.profile / "models.json", *(path for path, _ in credentials)]
        if (args.profile / "omp-models.json").is_file():
            profile_files.append(args.profile / "omp-models.json")
        profile_hashes = {str(path): digest(path) for path in profile_files}
        tasks = list(dict.fromkeys(args.task or MANIFEST["tasks"]))
        entries = schedule(list(candidates), tasks, args.rounds)
        sources = {str(path): digest(path) for path in HERE.iterdir() if path.suffix in (".py", ".json", ".lock")}
        plan = {"harbor_version": HARBOR_VERSION, "dataset": MANIFEST["dataset"], "dataset_commit": revision,
                "manifest_sha256": digest(HERE / "tasks.json"), "binaries": candidates,
                "adapter_sha256": sources,
                "model": args.model, "model_key": selected["default"], "reasoning_effort": args.reasoning_effort,
                "profile_sha256": profile_hashes, "rounds": args.rounds, "tasks": tasks,
                "concurrency": 1, "attempts_per_trial": 1, "harness_retries": 0,
                "sequence": entries, "commands": [command(entry, args, candidates, args.output) for entry in entries]}
        if args.dry_run:
            print(json.dumps(plan, indent=2))
            return
        if importlib.metadata.version("harbor") != HARBOR_VERSION:
            raise ValueError("Install the pinned benchmarks/harbor/requirements.lock")
        plan["runtime_images"] = check_images(tasks)
        plan["environment"] = {
            "python": platform.python_version(), "platform": platform.platform(),
            "docker": subprocess.check_output(["docker", "--version"], text=True).strip(),
            "compose": subprocess.check_output(["docker", "compose", "version"], text=True).strip(),
        }
    except (ValueError, OSError, KeyError, importlib.metadata.PackageNotFoundError) as error:
        parser.error(str(error))
    args.output.mkdir(parents=True)
    (args.output / "plan.json").write_text(json.dumps(plan, indent=2) + "\n")
    (args.output / "tmp").mkdir()
    env = dict(os.environ, PYTHONPATH=str(HERE), TMPDIR=str(args.output / "tmp"))
    try:
        for entry, argv in zip(entries, plan["commands"]):
            if any(digest(Path(name)) != expected for name, expected in profile_hashes.items()):
                raise RuntimeError("Source profile changed during the run")
            if any(digest(value["path"]) != value["sha256"] for value in candidates.values()):
                raise RuntimeError("A candidate binary changed during the run")
            if any(digest(path) != expected for path, expected in sources.items()):
                raise RuntimeError("Benchmark adapter changed during the run")
            check_dataset(args.dataset)
            check_images(tasks)
            (args.output / "progress.json").write_text(json.dumps(entry, indent=2) + "\n")
            print(json.dumps(entry), flush=True)
            with (args.output / (entry["job"] + ".console.log")).open("w") as log:
                result = subprocess.run(argv, cwd=REPO, env=env, stdout=log, stderr=subprocess.STDOUT)
            (args.output / (entry["job"] + ".exit.json")).write_text(json.dumps({"returncode": result.returncode}) + "\n")
    finally:
        summary = summarize_run(args.output)
        (args.output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
        print(json.dumps(summary["totals"], indent=2), flush=True)


if __name__ == "__main__":
    main()
