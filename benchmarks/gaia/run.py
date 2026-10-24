"""Prepare or run GAIA validation tasks through Harbor and Vista's HTTP API."""

import argparse
from datetime import datetime, timezone
import importlib.metadata
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import time

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
SHARED = HERE.parent / "harbor"
STATE = REPO / ".vista/benchmarks/gaia"
sys.path.insert(0, str(SHARED))

from benchmark_profile import binaries, digest, schedule, selected_profile
from dataset import SOURCES, check_dataset, prepare
from summarize import summarize_run

HARBOR_VERSION = "0.24.0"


def write_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.replace(path)


def select_tasks(manifest, tasks=None, levels=None):
    available = {row["task"]: row for row in manifest["tasks"]}
    if tasks and set(tasks) - available.keys():
        raise ValueError("Unknown GAIA --task ID")
    return [row for key, row in sorted(available.items())
            if (not tasks or key in tasks) and (not levels or row["level"] in levels)]


def command(entry, args, candidates):
    output = args.output
    result = [sys.executable, "-m", "harbor.cli.main", "run", "-p", str(args.dataset / entry["task"]),
              "-a", "gaia_agent:GAIAVistaAgent", "-m", args.model,
              "--ak", "binary_path=" + candidates[entry["label"]]["path"],
              "--ak", "profile_path=" + str(output / "profile"), "--ak", "model_key=" + args.model_key,
              "-n", "1", "-k", "1", "-r", "0", "-o", str(output / "jobs"), "--job-name", entry["job"]]
    if args.reasoning_effort is not None:
        result += ["--ak", "reasoning_effort=" + args.reasoning_effort]
    return result


def report(output, state, active=None):
    summary = summarize_run(output)
    write_json(output / "summary.json", summary)
    entries = json.loads((output / "plan.json").read_text())["sequence"]
    progress = {"state": state, "active": active, "planned": len(entries),
                "completed": sum((output / (entry["job"] + ".exit.json")).is_file() for entry in entries),
                "successes": sum(row["success"] for row in summary["trials"]), "levels": summary["levels"],
                "updated_at": datetime.now(timezone.utc).isoformat()}
    write_json(output / "progress.json", progress)
    return progress


def execute(output, plan):
    env = dict(os.environ, PYTHONPATH=str(output / "code"), TMPDIR=str(output / "tmp"), PYTHONDONTWRITEBYTECODE="1")
    state = "completed"
    try:
        for entry, argv in zip(plan["sequence"], plan["commands"]):
            if (output / "stop-after-current").exists():
                state = "stopped_between_trials"
                break
            if shutil.disk_usage(output).free < 2 * 1024 ** 3:
                raise RuntimeError("Insufficient free disk space; completed results are retained")
            if any(digest(path) != expected for path, expected in plan["frozen_sha256"].items()):
                raise RuntimeError("Frozen benchmark inputs changed")
            print(json.dumps(report(output, "running", entry)), flush=True)
            started = time.monotonic()
            with (output / (entry["job"] + ".console.log")).open("w") as log:
                result = subprocess.run(argv, cwd=REPO, env=env, stdout=log, stderr=subprocess.STDOUT)
            write_json(output / (entry["job"] + ".exit.json"), {"returncode": result.returncode,
                       "elapsed_s": round(time.monotonic() - started, 3)})
    except BaseException:
        state = "interrupted"
        raise
    finally:
        print(json.dumps(report(output, state)), flush=True)


def main(argv=None):
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare", action="store_true", help="Download pinned gated data and prepare tasks; no model calls")
    parser.add_argument("--offline", action="store_true", help="With --prepare, use only the Hugging Face cache")
    parser.add_argument("--cache", type=Path, default=STATE / "hf-cache")
    parser.add_argument("--dataset", type=Path, default=STATE / "dataset")
    parser.add_argument("--binary", action="append", metavar="LABEL=PATH")
    parser.add_argument("--profile", type=Path, help="Source profile; selected model, credentials and optional search.json are snapshotted")
    parser.add_argument("--model")
    parser.add_argument("--model-key")
    parser.add_argument("--reasoning-effort")
    parser.add_argument("--task", action="append", help="Select a validation task ID; repeat to select more")
    parser.add_argument("--level", action="append", type=int, choices=(1, 2, 3))
    parser.add_argument("--rounds", type=int, default=1)
    parser.add_argument("--output", type=Path, default=STATE / "runs" / datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ"))
    parser.add_argument("--dry-run", action="store_true", help="Validate prepared data and print the plan without Docker or model calls")
    args = parser.parse_args(argv)
    args.dataset, args.cache, args.output = args.dataset.resolve(), args.cache.resolve(), args.output.resolve()
    try:
        if args.prepare:
            if args.dry_run or args.binary or args.profile or args.model or args.task or args.level:
                raise ValueError("Run --prepare separately from model execution options")
            if not args.dataset.is_relative_to(REPO / ".vista") or not args.cache.is_relative_to(REPO / ".vista"):
                raise ValueError("Store GAIA data and cache under .vista/")
            manifest = prepare(args.dataset, args.cache, args.offline)
            print(json.dumps({"prepared": len(manifest["tasks"]), "dataset": str(args.dataset)}))
            return
        if args.offline or not args.binary or not args.profile or not args.model or args.rounds < 1:
            raise ValueError("Supply --binary LABEL=PATH, --profile and --model; --rounds must be positive. Use --offline only with --prepare")
        if not args.output.is_relative_to(REPO / ".vista") or args.output.exists():
            raise ValueError("Choose a new --output directory under .vista/")
        manifest = check_dataset(args.dataset)
        tasks = select_tasks(manifest, args.task, args.level)
        if not tasks:
            raise ValueError("No tasks match the selection")
        source_binaries = binaries(args.binary)
        args.profile = args.profile.resolve()
        selected, credentials = selected_profile(args.profile, args.model, args.model_key)
        args.model_key = selected["default"]
        profile_files = {relative: source for source, relative in credentials}
        for name in ("search.json", "omp-models.json"):
            if (args.profile / name).is_file():
                profile_files[name] = args.profile / name
        source_hashes = {str(path): digest(path) for path in [args.profile / "models.json", *profile_files.values()]}
        candidates = {label: {"path": str(args.output / "binary" / label), "sha256": value["sha256"]}
                      for label, value in source_binaries.items()}
        by_id = {row["task"]: row for row in tasks}
        entries = [{**entry, **by_id[entry["task"]]} for entry in schedule(list(candidates), list(by_id), args.rounds)]
        source_files = [*HERE.glob("*.py"), HERE / "sources.json", HERE / "requirements.lock",
                        *(SHARED / name for name in ("vista_agent.py", "vista_runner.py", "benchmark_profile.py", "summarize.py", "requirements.lock"))]
        adapter_hashes = {str(path): digest(path) for path in source_files}
        plan = {"suite": "gaia", "dataset": "GAIA validation", "dataset_commit": SOURCES["dataset"]["revision"],
                "official_scorer": SOURCES["scorer"], "manifest_sha256": digest(args.dataset / "manifest.json"),
                "harbor_version": HARBOR_VERSION, "model": args.model, "model_key": args.model_key,
                "reasoning_effort": args.reasoning_effort, "binaries": candidates, "source_binaries": source_binaries,
                "adapter_sha256": adapter_hashes, "source_profile_sha256": source_hashes,
                "tasks": len(tasks), "rounds": args.rounds, "concurrency": 1, "attempts_per_trial": 1,
                "harness_retries": 0, "agent_timeout_sec": 600, "sequence": entries,
                "scope": ("Full 165-task validation set." if len(tasks) == 165 else "Selected validation subset; not a full GAIA score.")
                         + " Network access is unrestricted; results are not contamination-controlled.",
                "commands": [command(entry, args, candidates) for entry in entries]}
        if args.dry_run:
            print(json.dumps(plan, indent=2))
            return
        if importlib.metadata.version("harbor") != HARBOR_VERSION:
            raise ValueError("Install benchmarks/gaia/requirements.lock")
        plan["environment"] = {"python": platform.python_version(), "platform": platform.platform(),
                               "docker": subprocess.check_output(["docker", "--version"], text=True).strip(),
                               "compose": subprocess.check_output(["docker", "compose", "version"], text=True).strip()}
    except (ValueError, OSError, KeyError, importlib.metadata.PackageNotFoundError) as error:
        parser.error(str(error))
    args.output.mkdir(parents=True, mode=0o700)
    for name in ("profile", "binary", "code", "tmp"):
        (args.output / name).mkdir(mode=0o700)
    write_json(args.output / "profile/models.json", selected)
    for relative, source in profile_files.items():
        target = args.output / "profile" / relative
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        shutil.copyfile(source, target)
        target.chmod(0o600)
    for label, source in source_binaries.items():
        shutil.copyfile(source["path"], candidates[label]["path"])
        Path(candidates[label]["path"]).chmod(0o700)
    for source in source_files:
        # Both lock files remain in the repository; only executable adapter sources are flattened.
        if source.suffix != ".lock":
            shutil.copyfile(source, args.output / "code" / source.name)
    if any(digest(path) != expected for path, expected in {**source_hashes, **adapter_hashes}.items()):
        raise RuntimeError("Source configuration or adapter changed while taking the snapshot")
    frozen = [*(args.output / "code").glob("*"), *(args.output / "binary").glob("*"),
              *(path for path in (args.output / "profile").glob("*") if path.is_file())]
    plan["frozen_sha256"] = {str(path): digest(path) for path in frozen}
    for label, candidate in candidates.items():
        if plan["frozen_sha256"][candidate["path"]] != candidate["sha256"]:
            raise RuntimeError("A candidate binary changed while taking the snapshot")
    plan["frozen_sha256"].update({str(args.dataset / name): value for name, value in manifest["files"].items()})
    plan["frozen_sha256"][str(args.dataset / "manifest.json")] = plan["manifest_sha256"]
    write_json(args.output / "plan.json", plan)
    execute(args.output, plan)


if __name__ == "__main__":
    main()
